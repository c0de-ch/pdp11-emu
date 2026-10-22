package panel

import (
	"context"
	"fmt"

	"pdp11emu/server/internal/api"
)

// inputSR sets the whole 22-bit switch register; bits 15:0 are live for
// programs at once.
func (p *Panel) inputSR(in api.PanelInput) string {
	if in.Int < 0 || in.Int > srMask {
		return fmt.Sprintf("Switch register value %d is out of range (22 bits).", in.Int)
	}
	v := uint32(in.Int)
	p.mu.Lock()
	changed := p.ctl.sr != v
	if changed {
		p.ctl.sr = v
		p.changedPersistentLocked()
	}
	p.mu.Unlock()
	if changed {
		p.srValue.Store(int32(v))
		signal(p.srReq)
		p.notify()
	}
	return ""
}

// inputMomentary tracks who holds a momentary switch. The action fires on
// the rising edge of the switch (first client to press it); releases only
// change the paddle.
func (p *Panel) inputMomentary(in api.PanelInput) string {
	var kind opKind
	switch in.Name {
	case swLoadAdrs:
		kind = opLoadAdrs
	case swExam:
		kind = opExam
	case swDep:
		kind = opDep
	case swCont:
		kind = opCont
	case swStart:
		kind = opStart
	case swLampTest:
		kind = -1
	default:
		return fmt.Sprintf("Unknown switch %q.", in.Name)
	}
	st := p.m.State() // before p.mu: never call the Machine under the lock

	p.mu.Lock()
	set := p.holders[in.Name]
	wasDown := len(set) > 0
	if in.Active {
		set[in.Client] = struct{}{}
	} else {
		delete(set, in.Client)
	}
	isDown := len(set) > 0
	hint := ""
	if !wasDown && isDown && kind >= 0 {
		hint = p.pressLocked(kind, in, st)
	}
	p.mu.Unlock()
	if wasDown != isDown {
		p.notify()
	}
	return hint
}

// pressLocked handles the press of a console switch. Caller holds p.mu.
func (p *Panel) pressLocked(kind opKind, in api.PanelInput, st api.RunState) string {
	snap := p.snapshotLocked()
	switch {
	case snap.key == keyOff:
		return hintMachineOff
	case snap.key == keyLock:
		return hintLocked
	case snap.sbus && (kind == opLoadAdrs || kind == opExam || kind == opDep):
		return hintSBus
	}
	if p.idleLocked() {
		// Nothing to wait for: answer from the current state right away.
		if h := stateHint(st); h != "" {
			return h
		}
		if (kind == opExam || kind == opDep) && p.cons.adrsErr {
			return hintAdrsErr
		}
	}
	return p.enqueueLocked(&op{kind: kind, client: in.Client, req: in.Req, snap: snap})
}

// inputLatch sets ENABLE/HALT or S INST/S BUS CYCLE. The paddle always
// moves; under LOCK its effect is forced to ENABLE.
func (p *Panel) inputLatch(in api.PanelInput) string {
	var down bool
	switch {
	case in.Name == "ENABLE_HALT" && (in.Value == "enable" || in.Value == "halt"):
		down = in.Value == "halt"
	case in.Name == "SINST_SBUS" && (in.Value == "inst" || in.Value == "bus"):
		down = in.Value == "bus"
	default:
		return fmt.Sprintf("Unknown switch setting %s=%q.", in.Name, in.Value)
	}
	p.mu.Lock()
	field := &p.ctl.sbus
	if in.Name == "ENABLE_HALT" {
		field = &p.ctl.halt
	}
	changed := *field != down
	before := p.ctl.effectiveHalt()
	*field = down
	if changed {
		p.changedPersistentLocked()
	}
	if !before && p.ctl.effectiveHalt() {
		p.requestHaltLocked()
	}
	locked := p.ctl.key == keyLock
	p.mu.Unlock()
	if !changed {
		return ""
	}
	p.notify()
	if locked {
		return hintLatchLocked
	}
	return ""
}

// inputKnob turns the ADDRESS or DATA knob to an absolute position.
func (p *Panel) inputKnob(in api.PanelInput) string {
	if in.Int < 0 || in.Int > 7 {
		return fmt.Sprintf("Knob position %d is out of range (0-7).", in.Int)
	}
	p.mu.Lock()
	var field *int
	switch in.Name {
	case "addr":
		field = &p.ctl.addrKnob
	case "data":
		field = &p.ctl.dataKnob
	default:
		p.mu.Unlock()
		return fmt.Sprintf("Unknown knob %q.", in.Name)
	}
	changed := *field != in.Int
	if changed {
		*field = in.Int
		p.changedPersistentLocked()
	}
	p.mu.Unlock()
	if changed {
		p.notify()
	}
	return ""
}

// inputKey turns the key switch. OFF pulls the plug at once (cancelling
// whatever is in flight); OFF → POWER/LOCK powers on and boots unless
// ENABLE/HALT is at HALT; POWER ↔ LOCK changes no power.
func (p *Panel) inputKey(in api.PanelInput) string {
	pos := in.Value
	switch pos {
	case keyOff, keyPower, keyLock:
	default:
		return fmt.Sprintf("Unknown key position %q.", pos)
	}
	p.mu.Lock()
	old := p.ctl.key
	if old == pos {
		p.mu.Unlock()
		return ""
	}
	before := p.ctl.effectiveHalt()
	p.ctl.key = pos
	p.changedPersistentLocked()
	hint := ""
	switch {
	case pos == keyOff:
		p.cutPowerLocked()
	case old == keyOff:
		hint = p.enqueueLocked(&op{kind: opPowerOn, client: in.Client, req: in.Req})
	default:
		if !before && p.ctl.effectiveHalt() {
			p.requestHaltLocked() // LOCK → POWER with the paddle at HALT
		}
	}
	p.mu.Unlock()
	p.notify()
	return hint
}

// cutPowerLocked implements the key at OFF: queued operations are dropped
// (the console lost power), the operation in flight is cancelled and the
// simulator is powered off from its own goroutine, ahead of anything
// queued later. Caller holds p.mu.
func (p *Panel) cutPowerLocked() {
	p.power = PowerStatus{}
	kept := p.queue[:0]
	for _, o := range p.queue {
		if o.external {
			kept = append(kept, o) // a power-off that is already running
		}
	}
	for i := len(kept); i < len(p.queue); i++ {
		p.queue[i] = nil
	}
	p.queue = kept
	if p.inflight != nil && p.inflight.kind != opPowerOff && p.inflight.cancel != nil {
		p.inflight.cancel()
	}
	if p.powerOff {
		return
	}
	o := &op{kind: opPowerOff, external: true, done: make(chan struct{})}
	p.queue = append(p.queue, o)
	p.powerOff = true
	signal(p.wake)
	ctx := p.baseContextLocked()
	go func() {
		defer close(o.done)
		pctx, cancel := context.WithTimeout(ctx, powerOffTimeout)
		defer cancel()
		if err := p.m.PowerOff(pctx); err != nil {
			p.log.Error("power off failed", "err", err)
		}
		p.mu.Lock()
		p.powerOff = false
		p.mu.Unlock()
		p.notify()
	}()
}

// inputBoot is the toolbar's Boot (PANEL_LOGIC.md §3 "boot").
func (p *Panel) inputBoot(in api.PanelInput) string {
	st := p.m.State()
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.ctl.key == keyLock:
		return hintBootLocked
	case p.ctl.effectiveHalt():
		return hintBootHalt
	case p.queuedLocked(opShutdown) || p.busyExtra != "":
		return hintShutdownActive
	case p.queuedLocked(opBoot) || st == api.RunStarting:
		return hintBootStarting
	}
	if p.ctl.key == keyOff {
		// Booting implies power: turn the key (the machine is off or about
		// to be); the boot operation powers on behind any power-off.
		p.ctl.key = keyPower
		p.changedPersistentLocked()
		p.notify()
	}
	return p.enqueueLocked(&op{kind: opBoot, client: in.Client, req: in.Req})
}

// inputShutdown starts the clean OS shutdown (shutdownOS).
func (p *Panel) inputShutdown(in api.PanelInput) string {
	st := p.m.State()
	if st != api.RunRunning {
		return hintShutdownIdle
	}
	if len(p.sys.ShutdownScript(p.runningSystem())) == 0 {
		return hintNoScript
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.queuedLocked(opShutdown) || p.busyExtra != "" {
		return hintShutdownActive
	}
	return p.enqueueLocked(&op{kind: opShutdown, client: in.Client, req: in.Req})
}

// inputRetryPower powers on after an install, selection or factory reset
// if the key is on and the machine is off.
func (p *Panel) inputRetryPower(in api.PanelInput) string {
	st := p.m.State()
	if st != api.RunOff && st != api.RunError {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctl.key == keyOff || p.queuedLocked(opPowerOn) || p.queuedLocked(opBoot) {
		return ""
	}
	return p.enqueueLocked(&op{kind: opPowerOn, req: in.Req})
}

// runningSystem is the id of the system the simulator runs (the selected
// one while off).
func (p *Panel) runningSystem() string {
	if id := p.m.SystemID(); id != "" {
		return id
	}
	return p.sys.Selected()
}
