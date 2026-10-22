package panel

import (
	"fmt"
	"time"

	"pdp11emu/server/internal/api"
)

// Boot-timeout warning (DESIGN §7 bootTimeoutSec, "warning toast only"):
// when a boot the panel started (power-on with boot, Machine.Boot from the
// toolbar, START at a boot-ROM entry point) has not reached the system's
// readyPattern within SystemInfo.BootTimeoutSec, every client gets one
// warning toast. The machine's readiness, any stop, power off and a new
// boot cancel the watch.
//
// The worker registers the watch before it calls the Machine, once the
// events of earlier operations have arrived (a stale stop would cancel
// it); the timer starts on the event goroutine when the panel sees the CPU
// running after that call. A restart for the boot (starting → halted →
// running) only pauses the watch.

// bootWatchUnit is the unit of BootTimeoutSec (a variable for tests).
var bootWatchUnit = time.Second

// bootWatch is one boot being watched. Guarded by p.mu.
type bootWatch struct {
	name  string // system name for the toast
	secs  int    // BootTimeoutSec
	req   string // correlation id of the input that booted
	timer *time.Timer
	armed uint64 // incremented by every arm: identifies the timer's callback
}

// expectBoot registers a watch for the boot the worker is about to start
// (the selected system boots). It returns nil when the system has no boot
// timeout. Called by the worker without p.mu.
func (p *Panel) expectBoot(o *op) *bootWatch {
	// Let the events of the operations before this one arrive first: a
	// stale EvStop or EvState(off) would cancel the new watch.
	p.waitObserved(o.ctx, stopWait)
	info, ok := p.sys.Get(p.sys.Selected())
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropBootWatchLocked()
	if !ok || info.BootTimeoutSec <= 0 {
		return nil
	}
	name := info.Name
	if name == "" {
		name = info.ID
	}
	p.bootWatch = &bootWatch{name: name, secs: info.BootTimeoutSec, req: o.req}
	return p.bootWatch
}

// cancelBootWatch drops bw if it is still the current watch (the boot
// call failed).
func (p *Panel) cancelBootWatch(bw *bootWatch) {
	if bw == nil {
		return
	}
	p.mu.Lock()
	if p.bootWatch == bw {
		p.dropBootWatchLocked()
	}
	p.mu.Unlock()
}

// dropBootWatchLocked ends the current watch. Caller holds p.mu.
func (p *Panel) dropBootWatchLocked() {
	if bw := p.bootWatch; bw != nil {
		if bw.timer != nil {
			bw.timer.Stop()
			bw.timer = nil
		}
		p.bootWatch = nil
	}
}

// bootWatchEventLocked applies a Machine event to the watch. Caller holds
// p.mu (onMachineEvent).
func (p *Panel) bootWatchEventLocked(ev api.MachineEvent) {
	bw := p.bootWatch
	if bw == nil {
		return
	}
	switch {
	case ev.Kind == api.EvState && ev.State == api.RunRunning:
		if bw.timer == nil {
			bw.armed++
			n := bw.armed
			bw.timer = time.AfterFunc(time.Duration(bw.secs)*bootWatchUnit, func() { p.bootWatchExpired(bw, n) })
		}
	case ev.Kind == api.EvState && (ev.State == api.RunStarting || ev.State == api.RunHalted):
		// (Re)starting the simulator for the boot: wait for it to run.
		if bw.timer != nil {
			bw.timer.Stop()
			bw.timer = nil
		}
	case ev.Kind == api.EvState, ev.Kind == api.EvStop, ev.Kind == api.EvReady:
		p.dropBootWatchLocked()
	}
}

// bootWatchExpired runs on the timer's goroutine.
func (p *Panel) bootWatchExpired(bw *bootWatch, n uint64) {
	p.mu.Lock()
	current := p.bootWatch == bw && bw.timer != nil && bw.armed == n
	if current {
		bw.timer = nil
		p.bootWatch = nil
	}
	p.mu.Unlock()
	if !current || p.m.Ready() || p.m.State() != api.RunRunning {
		return
	}
	p.log.Info("boot has not reached the ready prompt", "system", bw.name, "after", fmt.Sprintf("%d s", bw.secs))
	p.toastAll("warn", fmt.Sprintf("%s has not reached its prompt after %d s — check the console. If it hangs, power off and boot again.",
		bw.name, bw.secs), bw.req)
}
