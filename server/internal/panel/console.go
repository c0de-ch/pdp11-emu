package panel

import (
	"context"
	"errors"

	"pdp11emu/server/internal/api"
)

// Console address space constants (octal, as printed in the DEC manuals).
const (
	addrLow   = 0177777   // bits 15:0
	addrHigh  = 017600000 // bits 21:16 (SWR, latched by LOAD ADRS)
	grFirst   = 017777700 // R0 of register set 0: the general registers ...
	grLast    = 017777717 // ... up to USP
	grBarrier = 017777676 // EXAM/DEP-step from here into the registers is impossible
	bootROMA  = 0165000   // M9312 boot ROM entry points: START here boots the system [DEV]
	bootROMB  = 0173000
)

// grNames maps the low four bits of 17777700..17777717 to SimH register
// names, independent of the ADDRESS knob (KB11-C Table 2-1).
var grNames = [16]string{
	"R00", "R01", "R02", "R03", "R04", "R05", "KSP", "PC",
	"R10", "R11", "R12", "R13", "R14", "R15", "SSP", "USP",
}

type lastOp int

const (
	lastNone lastOp = iota
	lastExam
	lastDep
)

// console is the console (KY11-like) state of PANEL_LOGIC.md §2.
type console struct {
	addr      uint32 // console address register: SWR<21:16> + SR<15:0>
	lastOp    lastOp // EXAM/DEP step sequence
	adrsErr   bool   // static ADRS ERR after a failed console EXAM/DEP
	dataPaths uint16 // DATA PATHS while halted
	needR0    bool   // set dataPaths from the snapshot's R0 at the next frame (after a stop)
	consPA    uint32 // physical address of the last console EXAM/DEP (PROG PHY while halted)
	consPAOK  bool
	lastStop  string
	pc        uint16
}

func isGR(a uint32) bool { return a >= grFirst && a <= grLast }

// step returns the address after a in an EXAM/DEP step sequence. ok is
// false at 17777676, from where stepping into the registers is not
// possible (HB 11.5).
func step(a uint32) (next uint32, ok bool) {
	switch {
	case isGR(a):
		return grFirst + (a-grFirst+1)&017, true
	case a == grBarrier:
		return 0, false
	default:
		// Only the low 16 bits count: a step never leaves the 64 KB block.
		return a&^addrLow | (a+2)&addrLow, true
	}
}

// locate maps a console address to the location EXAM/DEP use for the
// given ADDRESS knob position.
func locate(a uint32, knob int) api.Location {
	if isGR(a) {
		return api.Location{Register: grNames[a&017]}
	}
	switch knob {
	case api.AddrProgPhy, api.AddrConsPhy:
		return api.Location{Addr: a &^ 1}
	}
	loc := api.Location{Addr: a & 0177776, Virtual: true}
	switch knob {
	case api.AddrKernelD:
		loc.Mode, loc.DSpace = api.ModeKernel, true
	case api.AddrSuperD:
		loc.Mode, loc.DSpace = api.ModeSuper, true
	case api.AddrUserD:
		loc.Mode, loc.DSpace = api.ModeUser, true
	case api.AddrUserI:
		loc.Mode = api.ModeUser
	case api.AddrSuperI:
		loc.Mode = api.ModeSuper
	case api.AddrKernelI:
		loc.Mode = api.ModeKernel
	}
	return loc
}

// stateHint explains why a console switch does nothing in Machine state
// st ("" when the CPU is halted).
func stateHint(st api.RunState) string {
	switch st {
	case api.RunHalted:
		return ""
	case api.RunRunning:
		return hintNotHalted
	case api.RunStarting:
		return hintStarting
	case api.RunStopping:
		return hintStopping
	default:
		return hintMachineOff
	}
}

// execLoadAdrs: consAddr = SR, ends a step sequence, clears ADRS ERR and
// DATA PATHS.
func (p *Panel) execLoadAdrs(o *op) {
	if h := stateHint(p.m.State()); h != "" {
		p.hint(o, h)
		return
	}
	p.mu.Lock()
	p.cons.addr = o.snap.sr
	p.cons.lastOp = lastNone
	p.cons.adrsErr = false
	p.cons.dataPaths = 0
	p.cons.needR0 = false
	p.mu.Unlock()
	p.notify()
}

// execExamDep implements EXAM and DEP with the step and ADRS ERR rules.
func (p *Panel) execExamDep(o *op) {
	if h := stateHint(p.m.State()); h != "" {
		p.hint(o, h)
		return
	}
	dep := o.kind == opDep
	repeat := lastExam
	if dep {
		repeat = lastDep
	}

	p.mu.Lock()
	if p.cons.adrsErr {
		p.mu.Unlock()
		p.hint(o, hintAdrsErr)
		return
	}
	gen := p.consGen
	prevAddr, prevOp := p.cons.addr, p.cons.lastOp
	addr, stepped := prevAddr, false
	if prevOp == repeat {
		next, ok := step(addr)
		if !ok {
			p.cons.adrsErr = true
			p.cons.addr = o.snap.sr
			p.cons.lastOp = lastNone
			p.mu.Unlock()
			p.notify()
			return
		}
		addr, stepped = next, true
		p.cons.addr = addr
	}
	p.mu.Unlock()
	if stepped {
		p.notify()
	}

	ctx, cancel := context.WithTimeout(o.ctx, consoleOpTimeout)
	defer cancel()
	loc := locate(addr, o.snap.addrKnob)
	value := uint16(o.snap.sr & addrLow)
	var err error
	if dep {
		err = p.m.Deposit(ctx, loc, value)
	} else {
		value, err = p.m.Examine(ctx, loc)
	}
	var pa uint32
	paOK := false
	if err == nil && loc.Register == "" {
		if loc.Virtual {
			if v, terr := p.m.Translate(ctx, loc); terr == nil {
				pa, paOK = v, true
			} else {
				p.log.Debug("console translate failed", "loc", loc, "err", terr)
			}
		} else {
			pa, paOK = loc.Addr, true
		}
	}

	var hint string
	p.mu.Lock()
	if p.consGen != gen {
		// A stop or a power-on rewrote the console state meanwhile.
		p.mu.Unlock()
		return
	}
	switch {
	case err == nil:
		p.cons.dataPaths = value
		p.cons.needR0 = false
		p.cons.lastOp = repeat
		p.cons.consPA, p.cons.consPAOK = pa, paOK
	case errors.Is(err, api.ErrAddress):
		p.cons.adrsErr = true
		p.cons.lastOp = lastNone
		p.cons.consPAOK = false
		if !stepped {
			p.cons.addr &^= addrLow // observed on a real 11/70 (LCM 2018)
		}
	default:
		// Not an address problem (e.g. another viewer resumed the CPU):
		// the operation did not happen, so neither did its step.
		p.cons.addr, p.cons.lastOp = prevAddr, prevOp
		hint = consoleErrHint(err)
		p.log.Debug("console operation failed", "op", o.name(), "loc", loc, "err", err)
	}
	p.mu.Unlock()
	p.notify()
	p.hint(o, hint)
}

// execCont: ENABLE → Continue; HALT → single instruction (also in S BUS
// CYCLE [DEV]).
func (p *Panel) execCont(o *op) {
	if h := stateHint(p.m.State()); h != "" {
		p.hint(o, h)
		return
	}
	ctx, cancel := context.WithTimeout(o.ctx, consoleOpTimeout)
	defer cancel()
	var err error
	if o.snap.effHalt {
		since := p.stopCount()
		if err = p.m.Step(ctx); err == nil {
			p.cpuStarted()
			p.waitStop(ctx, since, stopWait)
		}
	} else if err = p.m.Continue(ctx); err == nil {
		p.cpuStarted()
	}
	if err != nil {
		p.log.Debug("CONT failed", "err", err)
		p.hint(o, consoleErrHint(err))
	}
}

// execStart: HALT → console reset; ENABLE → Start at the console address,
// or boot the selected system at the boot ROM entry points [DEV].
func (p *Panel) execStart(o *op) {
	if h := stateHint(p.m.State()); h != "" {
		p.hint(o, h)
		return
	}
	var err error
	if o.snap.effHalt {
		ctx, cancel := context.WithTimeout(o.ctx, consoleOpTimeout)
		defer cancel()
		err = p.m.ConsoleReset(ctx)
	} else {
		p.mu.Lock()
		a := uint16(p.cons.addr & addrLow)
		p.mu.Unlock()
		if a == bootROMA || a == bootROMB {
			ctx, cancel := context.WithTimeout(o.ctx, bootTimeout)
			defer cancel()
			bw := p.expectBoot(o)
			if err = p.m.Boot(ctx); err != nil {
				p.cancelBootWatch(bw)
			}
		} else {
			ctx, cancel := context.WithTimeout(o.ctx, consoleOpTimeout)
			defer cancel()
			err = p.m.Start(ctx, a)
		}
	}
	if err != nil {
		p.log.Debug("START failed", "err", err)
		p.hint(o, consoleErrHint(err))
		return
	}
	p.cpuStarted()
}

// cpuStarted clears the static console indications when the CPU starts
// (CONT, START, boot, step).
func (p *Panel) cpuStarted() {
	p.mu.Lock()
	changed := p.cons.adrsErr
	p.cons.adrsErr = false
	p.cons.consPAOK = false
	p.mu.Unlock()
	if changed {
		p.notify()
	}
}

// consoleErrHint turns a Machine error into a hint for the sender.
func consoleErrHint(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return ""
	case errors.Is(err, api.ErrNotHalted):
		return hintNotHalted
	case errors.Is(err, api.ErrNotPowered):
		return hintMachineOff
	case errors.Is(err, api.ErrBusy):
		return hintMachineBusy
	}
	return "The console operation failed: " + err.Error()
}
