package panel

import (
	"context"
	"math/bits"
	"time"

	"pdp11emu/server/internal/api"
)

// Lamp frame computation (PANEL_LOGIC.md §4). Lamp values are linear duty
// cycles 0..255; the browser smooths and gamma-corrects them.

// knobLamps maps ADDRESS knob positions to their position lamp.
var addrKnobLamps = [8]int{
	api.AddrProgPhy: api.LampProgPhy,
	api.AddrConsPhy: api.LampConsPhy,
	api.AddrKernelD: api.LampKernelD,
	api.AddrSuperD:  api.LampSuperD,
	api.AddrUserD:   api.LampUserD,
	api.AddrUserI:   api.LampUserI,
	api.AddrSuperI:  api.LampSuperI,
	api.AddrKernelI: api.LampKernelI,
}

// dataFuncLamps maps DATA knob functions (position % 4) to their lamp.
var dataFuncLamps = [4]int{
	api.DataBusReg:     api.LampBusReg,
	api.DataDataPaths:  api.LampDataPaths,
	api.DataUAdrs:      api.LampUAdrs,
	api.DataDisplayReg: api.LampDisplayReg,
}

// uAdrsRunning stands in for the micro-PC while running [DEV]: the five
// addresses REALCONS took from a trace of the real machine.
var uAdrsRunning = [5]uint16{0x3bf, 0x32a, 0x3bd, 0x32c, 0x3f5}

const (
	uAdrsWait   = 0x3bf
	uAdrsHalted = 0170 // CON.00: the console loop
	runDuty     = 0.92 // RUN is off during the pause cycles of running code
	pauseDuty   = 0.5  // PAUSE relative to MASTER
)

// frameState is what the lamp engine needs from the panel for one frame.
type frameState struct {
	key      string
	run      api.RunState
	lampTest bool
	addrKnob int
	dataFunc int
	sr       uint32
	cons     console
}

// lampEngine turns samples and snapshots into lamp frames. It keeps the
// state that spans frames; only the frame loop uses it.
type lampEngine struct {
	prev     [api.LampCount]byte // the previous computed (not lamp-tested) frame
	snap     api.PanelSnapshot   // the latest good snapshot
	trap     uint32              // TrapCount at the previous frame
	haveTrap bool
	parHi    bool // parity lamps of the latest non-WAIT sample
	parLo    bool
	acc      [api.LampCount]float64
	drOn     [16]uint32 // DISPLAY REGISTER accumulators at the previous frame
	drSum    uint32
	haveDR   bool
}

// drFrame is the time-weighted DISPLAY REGISTER of one frame: on[i] of sum
// (wall µs) with bit i set; sum is 0 when no sample was taken.
type drFrame struct {
	on  [16]uint32
	sum uint32
}

// reset forgets everything from the previous power cycle.
func (e *lampEngine) reset() { *e = lampEngine{} }

// compute produces the lamp frame for one frame period.
func (e *lampEngine) compute(fs *frameState, samples []api.Sample, snap api.PanelSnapshot, ok bool, out *[api.LampCount]byte) {
	if fs.key == keyOff {
		e.reset()
		*out = [api.LampCount]byte{} // no power
		return
	}
	if fs.run != api.RunHalted && fs.run != api.RunRunning {
		// Off, starting, stopping or in error: the CPU section is dark;
		// the knob lamps need only console power.
		e.reset()
		*out = [api.LampCount]byte{}
		knobLamps(fs, out)
		if fs.lampTest {
			fill(out, 255) // lamps need power: any key position but OFF [DEV]
		}
		return
	}
	var dr drFrame
	if ok {
		dr = e.displayRegister(&snap)
		e.snap = snap
	}

	var f [api.LampCount]byte
	if fs.run == api.RunHalted {
		e.halted(fs, &f)
	} else {
		e.running(fs, samples, &dr, &f)
	}
	// ADRS ERR: static after a console error while halted; a flash in a
	// frame in which the trap counter moved while running.
	trapped := ok && e.haveTrap && snap.TrapCount != e.trap
	if ok {
		e.trap, e.haveTrap = snap.TrapCount, true
	}
	switch {
	case fs.run == api.RunHalted && fs.cons.adrsErr, fs.run == api.RunRunning && trapped:
		f[api.LampAdrsErr] = 255
	default:
		f[api.LampAdrsErr] = 0
	}
	f[api.LampParErr] = 0 // [DEV] no parity errors
	knobLamps(fs, &f)
	e.prev = f
	*out = f
	if fs.lampTest {
		fill(out, 255)
	}
}

// knobLamps lights the selected ADDRESS position and DATA function. The
// switches drive these lamps directly.
func knobLamps(fs *frameState, f *[api.LampCount]byte) {
	for i := api.LampUserD; i <= api.LampDisplayReg; i++ {
		f[i] = 0
	}
	f[addrKnobLamps[fs.addrKnob&7]] = 255
	f[dataFuncLamps[fs.dataFunc&3]] = 255
}

// displayRegister takes the differences of the export's DISPLAY REGISTER
// accumulators since the previous frame (modulo 2^32) and keeps the new
// values as the baseline.
func (e *lampEngine) displayRegister(snap *api.PanelSnapshot) drFrame {
	var d drFrame
	if e.haveDR {
		d.sum = snap.WeightSum - e.drSum
		for i := range d.on {
			d.on[i] = snap.DROn[i] - e.drOn[i]
		}
	}
	e.drOn, e.drSum, e.haveDR = snap.DROn, snap.WeightSum, true
	return d
}

// halted renders the console display (§4.2).
func (e *lampEngine) halted(fs *frameState, f *[api.LampCount]byte) {
	c := &fs.cons
	var a uint32
	switch {
	case isGR(c.addr):
		// General register: bits 21:06 on, 05:04 off, 03:00 = register (any knob).
		a = 017777700 | c.addr&017
	case fs.addrKnob == api.AddrConsPhy:
		a = c.addr
	case fs.addrKnob == api.AddrProgPhy:
		if c.consPAOK {
			a = c.consPA
		} else {
			a = e.snap.LastPA & api.PnlPAMask
		}
	default:
		a = c.addr & addrLow
	}
	setBits(f, api.LampA0, a, 22)

	var d uint16
	switch fs.dataFunc {
	case api.DataDataPaths:
		d = c.dataPaths
	case api.DataBusReg:
		d = uint16(fs.sr & addrLow)
	case api.DataUAdrs:
		d = uAdrsHalted
	case api.DataDisplayReg:
		d = e.snap.DR
	}
	setBits(f, api.LampD0, uint32(d), 16)

	f[api.LampMaster] = 255 // the console flag
	switch e.snap.PSW >> 14 {
	case uint16(api.ModeKernel):
		f[api.LampKernel] = 255
	case uint16(api.ModeSuper):
		f[api.LampSuper] = 255
	case uint16(api.ModeUser):
		f[api.LampUser] = 255
	}
	switch {
	case e.snap.MMR0&1 == 0:
		f[api.LampAdrs16] = 255
	case e.snap.MMR3&(1<<4) == 0:
		f[api.LampAdrs18] = 255
	default:
		f[api.LampAdrs22] = 255
	}
	// RUN, PAUSE, DATA, PARITY stay 0.
}

// running weights every sample by its wall-clock duration (§4.1).
func (e *lampEngine) running(fs *frameState, samples []api.Sample, dr *drFrame, f *[api.LampCount]byte) {
	acc := &e.acc
	*acc = [api.LampCount]float64{}
	var total float64
	consHigh := fs.cons.addr & addrHigh
	for i := range samples {
		s := &samples[i]
		w := float64(s.Weight)
		if w < 1 {
			w = 1
		}
		total += w
		wait := s.Addr&api.PnlWait != 0

		var a uint32
		switch fs.addrKnob {
		case api.AddrProgPhy:
			a = s.Addr & api.PnlPAMask
		case api.AddrConsPhy:
			a = uint32(s.VA) | consHigh
		default:
			a = uint32(s.VA) // bits 21:16 dark
		}
		addBits(acc, api.LampA0, a, w)

		switch fs.dataFunc {
		case api.DataDataPaths:
			addBits(acc, api.LampD0, uint32(s.Data), w) // WAIT samples carry R0
		case api.DataBusReg:
			d := s.Data
			if wait {
				d = e.snap.LastData
			}
			addBits(acc, api.LampD0, uint32(d), w)
		case api.DataUAdrs:
			d := uint16(uAdrsWait)
			if !wait {
				d = uAdrsRunning[(s.PC^s.IR)%5]
			}
			addBits(acc, api.LampD0, uint32(d), w)
		}

		switch (s.Addr >> api.PnlVMode) & api.PnlMMode {
		case uint32(api.ModeKernel):
			acc[api.LampKernel] += w
		case uint32(api.ModeSuper):
			acc[api.LampSuper] += w
		case uint32(api.ModeUser):
			acc[api.LampUser] += w
		}
		if s.Addr&api.PnlDSpace != 0 {
			acc[api.LampData] += w
		}
		switch {
		case s.Addr&api.PnlMMU == 0:
			acc[api.LampAdrs16] += w
		case s.Addr&api.PnlM22 == 0:
			acc[api.LampAdrs18] += w
		default:
			acc[api.LampAdrs22] += w
		}

		if !wait {
			e.parHi, e.parLo = parity(s)
		}
		if e.parHi {
			acc[api.LampParityHigh] += w
		}
		if e.parLo {
			acc[api.LampParityLow] += w
		}

		if wait {
			acc[api.LampRun] += w
		} else {
			acc[api.LampRun] += runDuty * w
			if s.Addr&api.PnlIOPage != 0 {
				acc[api.LampMaster] += w
				acc[api.LampPause] += pauseDuty * w
			}
		}
	}

	if total == 0 {
		// No new samples: repeat the previous frame (the CPU idles between
		// clock ticks), except what follows the snapshot.
		*f = e.prev
	} else {
		for i := 0; i < api.LampCount; i++ {
			f[i] = duty(acc[i], total)
		}
	}
	if fs.dataFunc == api.DataDisplayReg {
		// The DR of the samples, weighted like them; the latch itself in a
		// frame without samples.
		if dr.sum == 0 {
			setBits(f, api.LampD0, uint32(e.snap.DR), 16)
		} else {
			for i := range dr.on {
				f[api.LampD0+i] = duty(float64(dr.on[i]), float64(dr.sum))
			}
		}
	}
}

// parity returns the PARITY HIGH/LOW lamps for a non-WAIT sample: memory
// reads light a byte's lamp when the byte has an even number of ones;
// writes and I/O-page cycles clear both. A byte read only knows its own
// byte lane (the other lamp is off).
func parity(s *api.Sample) (hi, lo bool) {
	if s.Addr&(api.PnlWR|api.PnlIOPage) != 0 {
		return false, false
	}
	even := func(b uint16) bool { return bits.OnesCount8(uint8(b))%2 == 0 }
	if s.Addr&api.PnlByte != 0 {
		if s.Addr&1 != 0 {
			return even(s.Data), false
		}
		return false, even(s.Data)
	}
	return even(s.Data >> 8), even(s.Data)
}

func duty(on, total float64) byte {
	v := on*255/total + 0.5
	if v >= 255 {
		return 255
	}
	if v <= 0 {
		return 0
	}
	return byte(v)
}

// addBits adds w to the accumulator of every set bit of v (lamps from base).
func addBits(acc *[api.LampCount]float64, base int, v uint32, w float64) {
	for v != 0 {
		b := bits.TrailingZeros32(v)
		acc[base+b] += w
		v &= v - 1
	}
}

// setBits sets the n lamps from base to the bits of v (on = 255).
func setBits(f *[api.LampCount]byte, base int, v uint32, n int) {
	for i := 0; i < n; i++ {
		if v&(1<<i) != 0 {
			f[base+i] = 255
		} else {
			f[base+i] = 0
		}
	}
}

func fill(f *[api.LampCount]byte, v byte) {
	for i := range f {
		f[i] = v
	}
}

// frameKeepalive: frames are sent while lamps change, and at least this
// often (a variable for tests).
var frameKeepalive = time.Second

// frameLoop computes a lamp frame every 1/FrameHz and delivers it when
// it changed (or once per second).
func (p *Panel) frameLoop(ctx context.Context) {
	t := time.NewTicker(time.Second / time.Duration(p.opts.FrameHz))
	defer t.Stop()
	var (
		frame    [api.LampCount]byte
		sent     [api.LampCount]byte
		sentAt   time.Time
		seq      uint16
		haveSent bool
		deliver  = new([api.LampCount]byte) // what subscribers see (allocated once)
	)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			p.computeFrame(&frame)
			if haveSent && frame == sent && now.Sub(sentAt) < frameKeepalive {
				continue
			}
			seq++
			sent, sentAt, haveSent = frame, now, true
			p.frames.each(func(fn func(*[api.LampCount]byte, uint16)) {
				*deliver = frame // a subscriber cannot corrupt the frame for the next one
				fn(deliver, seq)
			})
		}
	}
}

// computeFrame reads the export and computes one frame.
func (p *Panel) computeFrame(out *[api.LampCount]byte) {
	var (
		n    int
		snap api.PanelSnapshot
		ok   bool
	)
	if p.src != nil {
		n, snap, ok = p.src.ReadFrame(p.samples)
		if n > len(p.samples) {
			n = len(p.samples)
		}
	}
	st := p.m.State()

	p.mu.Lock()
	if ok && p.cons.needR0 && st == api.RunHalted {
		p.cons.dataPaths = snap.R[0]
		p.cons.needR0 = false
	}
	fs := frameState{
		key:      p.ctl.key,
		run:      st,
		lampTest: len(p.holders[swLampTest]) > 0,
		addrKnob: p.ctl.addrKnob,
		dataFunc: p.ctl.dataKnob % 4,
		sr:       p.ctl.sr,
		cons:     p.cons,
	}
	p.mu.Unlock()

	p.eng.compute(&fs, p.samples[:n], snap, ok, out)
}
