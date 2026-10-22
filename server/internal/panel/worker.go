package panel

import (
	"context"
	"errors"
	"time"

	"pdp11emu/server/internal/api"
)

type opKind int

const (
	opLoadAdrs opKind = iota
	opExam
	opDep
	opCont
	opStart
	opHalt
	opPowerOn
	opPowerOff
	opBoot
	opShutdown
)

// pressSnap is the state of the controls when a switch was pressed: an
// action that waited in the queue behaves as the switches were set when it
// was pressed, while the CPU state is the one it finds when it runs.
type pressSnap struct {
	sr       uint32
	effHalt  bool
	sbus     bool
	addrKnob int
	key      string
}

// op is one queued Machine operation.
type op struct {
	kind   opKind
	client uint64 // 0: not caused by a client (autostart, level rule, web)
	req    string
	snap   pressSnap
	boot   bool // opPowerOn: boot regardless of ENABLE/HALT (toolbar boot)

	// external ops (the pre-empting power-off) run on their own goroutine;
	// the worker only waits for done to keep later operations behind them.
	external bool
	done     chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
}

// long operations can take seconds to minutes: HALT bypasses them.
func (o *op) long() bool {
	switch o.kind {
	case opPowerOn, opPowerOff, opBoot, opShutdown:
		return true
	}
	return false
}

// name is the ConsoleInfo.Busy text of the operation.
func (o *op) name() string {
	switch o.kind {
	case opLoadAdrs:
		return "load address"
	case opExam:
		return "examine"
	case opDep:
		return "deposit"
	case opCont:
		if o.snap.effHalt {
			return "step"
		}
		return "continue"
	case opStart:
		if o.snap.effHalt {
			return "reset"
		}
		return "start"
	case opHalt:
		return "halt"
	case opPowerOn:
		return "power on"
	case opPowerOff:
		return "power off"
	case opBoot:
		return "boot"
	case opShutdown:
		return "shutdown"
	}
	return "operation"
}

// snapshotLocked captures the controls for a queued action. Caller holds p.mu.
func (p *Panel) snapshotLocked() pressSnap {
	return pressSnap{
		sr:       p.ctl.sr,
		effHalt:  p.ctl.effectiveHalt(),
		sbus:     p.ctl.sbus,
		addrKnob: p.ctl.addrKnob,
		key:      p.ctl.key,
	}
}

// idleLocked reports whether nothing is queued or running, so that an
// input can be judged against the current Machine state right away.
func (p *Panel) idleLocked() bool {
	return p.inflight == nil && len(p.queue) == 0 && !p.powerOff && !p.sideHalt && p.busyExtra == ""
}

// busyLocked names the operation in flight ("" if none).
func (p *Panel) busyLocked() string {
	switch {
	case p.powerOff:
		return "power off"
	case p.busyExtra != "":
		return p.busyExtra
	case p.inflight != nil:
		return p.inflight.name()
	}
	return ""
}

// enqueueLocked queues an operation. HALT goes first: the HALT level must
// take effect before queued console actions. Caller holds p.mu.
func (p *Panel) enqueueLocked(o *op) string {
	if len(p.queue) >= maxQueuedOps && o.kind != opHalt { // the HALT level is never lost
		p.log.Warn("panel operation queue is full; input ignored", "op", o.name())
		return hintQueueFull
	}
	if o.kind == opHalt {
		p.queue = append([]*op{o}, p.queue...)
	} else {
		p.queue = append(p.queue, o)
	}
	signal(p.wake)
	return ""
}

// queuedLocked reports whether an operation of kind k is queued or running.
func (p *Panel) queuedLocked(k opKind) bool {
	if p.inflight != nil && p.inflight.kind == k {
		return true
	}
	for _, o := range p.queue {
		if o.kind == k {
			return true
		}
	}
	return false
}

func (p *Panel) workerLoop(ctx context.Context) {
	for {
		o := p.dequeue(ctx)
		if o == nil {
			return
		}
		if o.external {
			select {
			case <-o.done:
			case <-ctx.Done():
			}
		} else {
			p.execute(o)
		}
		p.mu.Lock()
		p.inflight = nil
		p.mu.Unlock()
		p.notify()
	}
}

// dequeue waits for the next operation and marks it in flight.
func (p *Panel) dequeue(ctx context.Context) *op {
	for {
		p.mu.Lock()
		if len(p.queue) > 0 {
			o := p.queue[0]
			p.queue[0] = nil
			p.queue = p.queue[1:]
			if !o.external {
				o.ctx, o.cancel = context.WithCancel(ctx)
			}
			p.inflight = o
			p.mu.Unlock()
			p.notify()
			return o
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-p.wake:
		}
	}
}

// acquireExec serializes Machine operations of the worker and ShutdownOS.
func (p *Panel) acquireExec(ctx context.Context) error {
	select {
	case p.execSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Panel) releaseExec() { <-p.execSem }

func (p *Panel) execute(o *op) {
	defer o.cancel()
	if err := p.acquireExec(o.ctx); err != nil {
		return
	}
	defer p.releaseExec()
	switch o.kind {
	case opLoadAdrs:
		p.execLoadAdrs(o)
	case opExam, opDep:
		p.execExamDep(o)
	case opCont:
		p.execCont(o)
	case opStart:
		p.execStart(o)
	case opHalt:
		p.execHalt(o)
	case opPowerOn:
		p.execPowerOn(o)
	case opBoot:
		p.execBoot(o)
	case opShutdown:
		if err := p.shutdown(o, true); err != nil {
			p.log.Debug("OS shutdown not started", "err", err)
		}
	}
}

// requestHaltLocked applies the ENABLE/HALT level: the CPU must not run
// while effectiveHalt is true. Behind a long operation the halt is issued
// at once from its own goroutine; otherwise it is queued first. Either way
// it does nothing unless the CPU is running when it executes. Caller holds
// p.mu.
func (p *Panel) requestHaltLocked() {
	if (p.inflight != nil && p.inflight.long()) || p.busyExtra != "" {
		if p.sideHalt {
			return
		}
		p.sideHalt = true
		ctx := p.baseContextLocked()
		go func() {
			defer func() {
				p.mu.Lock()
				p.sideHalt = false
				p.mu.Unlock()
				p.notify()
			}()
			hctx, cancel := context.WithTimeout(ctx, haltTimeout)
			defer cancel()
			if p.m.State() == api.RunRunning {
				if err := p.m.Halt(hctx); err != nil {
					p.log.Warn("halt failed", "err", err)
				}
			}
		}()
		return
	}
	for _, o := range p.queue {
		if o.kind == opHalt {
			return
		}
	}
	p.enqueueLocked(&op{kind: opHalt})
}

func (p *Panel) execHalt(o *op) {
	if p.m.State() != api.RunRunning {
		return
	}
	ctx, cancel := context.WithTimeout(o.ctx, haltTimeout)
	defer cancel()
	since := p.stopCount()
	if err := p.m.Halt(ctx); err != nil {
		if !errors.Is(err, context.Canceled) {
			p.log.Warn("halt failed", "err", err)
		}
		return
	}
	p.waitStop(ctx, since, stopWait)
}

// stopCount returns the number of stops seen so far.
func (p *Panel) stopCount() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopSeq
}

// waitStop waits until a stop after since was processed, so that the next
// queued console action sees the console address the stop set.
func (p *Panel) waitStop(ctx context.Context, since uint64, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		p.mu.Lock()
		seq, ch := p.stopSeq, p.evSignal
		p.mu.Unlock()
		if seq > since {
			return true
		}
		select {
		case <-ch:
		case <-t.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// waitObserved waits until the panel processed the event of the Machine's
// current state. Events arrive asynchronously: after PowerOn returned, the
// console initialization of EvState(starting) must not overwrite what the
// next queued console action does.
func (p *Panel) waitObserved(ctx context.Context, timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	for {
		st := p.m.State()
		p.mu.Lock()
		seen, ch := p.observed, p.evSignal
		p.mu.Unlock()
		if seen == st {
			return true
		}
		select {
		case <-ch:
		case <-t.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// onMachineEvent runs on the Machine's event goroutine: it must not block
// and must not call Machine methods.
func (p *Panel) onMachineEvent(ev api.MachineEvent) {
	p.mu.Lock()
	changed := false
	switch ev.Kind {
	case api.EvState:
		p.observed = ev.State
		switch ev.State {
		case api.RunStarting:
			// Every power-on (and every reboot restart) initializes the console.
			p.cons = console{}
			p.consGen++
			changed = true
		case api.RunRunning:
			if p.ctl.effectiveHalt() {
				p.requestHaltLocked()
			}
		}
	case api.EvStop:
		var s api.StopInfo
		if ev.Stop != nil {
			s = *ev.Stop
		}
		p.cons.addr = p.cons.addr&addrHigh | uint32(s.PC)
		p.cons.lastOp = lastNone
		p.cons.needR0 = true // [DEV] DATA PATHS shows R0 after every stop
		p.cons.lastStop = s.Reason
		p.cons.pc = s.PC
		p.consGen++
		p.stopSeq++
		p.lastStop = s
		changed = true
	}
	p.bootWatchEventLocked(ev)
	close(p.evSignal)
	p.evSignal = make(chan struct{})
	p.mu.Unlock()
	if changed {
		p.notify()
	}
}
