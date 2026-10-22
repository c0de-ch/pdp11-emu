package panel

import (
	"math/bits"
	"math/rand/v2"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel/paneltest"
)

// Sample constructors. Flags are api.Pnl* bits; mode is api.CPUMode.
func modeBits(m api.CPUMode) uint32 { return uint32(m) << api.PnlVMode }

func busSample(pa uint32, data, va uint16, w uint32, flags uint32) api.Sample {
	return api.Sample{Addr: pa&api.PnlPAMask | flags, Data: data, VA: va, Weight: w, PC: va, IR: 0o12700}
}

func waitSample(pa uint32, r0, pc uint16, w uint32, flags uint32) api.Sample {
	return api.Sample{Addr: pa&api.PnlPAMask | api.PnlWait | flags, Data: r0, VA: pc, Weight: w, PC: pc, IR: 1}
}

func runningState(addrKnob, dataKnob int) *frameState {
	return &frameState{key: keyPower, run: api.RunRunning, addrKnob: addrKnob, dataFunc: dataKnob % 4}
}

// field reads n lamps from base as an integer: a lamp counts as lit at >= min.
func field(f *[api.LampCount]byte, base, n int, min byte) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		if f[base+i] >= min {
			v |= 1 << i
		}
	}
	return v
}

func near(got, want byte, tol int) bool {
	d := int(got) - int(want)
	return d >= -tol && d <= tol
}

func checkLamp(t *testing.T, f *[api.LampCount]byte, idx int, want byte, tol int, what string) {
	t.Helper()
	if !near(f[idx], want, tol) {
		t.Errorf("%s (lamp %d) = %d, want %d±%d", what, idx, f[idx], want, tol)
	}
}

// bsdIdleFrame is one 60 Hz frame of 2.11BSD idling: the CPU sits in WAIT
// for ~95% of the time with the rotating idle pattern in R0 (rdisply),
// interrupted by the clock handler (many short instruction samples with
// unrelated data).
func bsdIdleFrame(rng *rand.Rand, pattern uint16) []api.Sample {
	const pcPA, pc = 0o1120, 0o1120
	flags := api.PnlMMU | api.PnlM22 | modeBits(api.ModeKernel)
	s := []api.Sample{waitSample(pcPA, pattern, pc, 15900, flags)}
	for i := 0; i < 200; i++ {
		f := flags
		if rng.IntN(4) == 0 {
			f |= api.PnlWR
		}
		s = append(s, busSample(uint32(rng.IntN(0o1000000)), uint16(rng.Uint32()), uint16(rng.Uint32()), 4, f))
	}
	rng.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
	return s
}

func TestIdlePatternCrispInDataPaths(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	var e lampEngine
	fs := runningState(api.AddrProgPhy, api.DataDataPaths)
	var f [api.LampCount]byte
	// 2.11BSD's rdisply: 0377 rotated left (a snake of 8 lights).
	pattern := uint16(0o377)
	for frame := 0; frame < 16; frame++ {
		e.compute(fs, bsdIdleFrame(rng, pattern), api.PanelSnapshot{}, true, &f)
		for b := 0; b < 16; b++ {
			lit := pattern&(1<<b) != 0
			v := f[api.LampD0+b]
			if lit && v < 235 || !lit && v > 20 {
				t.Fatalf("frame %d pattern %06o: DATA bit %d = %d (lit %v)", frame, pattern, b, v, lit)
			}
		}
		// ADDRESS (PROG PHY) sits on the PA of the PC after the WAIT.
		if got := field(&f, api.LampA0, 22, 235); got != 0o1120 {
			t.Fatalf("ADDRESS bright lamps = %o, want 1120", got)
		}
		checkLamp(t, &f, api.LampRun, 254, 1, "RUN")
		checkLamp(t, &f, api.LampKernel, 255, 0, "KERNEL")
		checkLamp(t, &f, api.LampAdrs22, 255, 0, "ADRS22")
		checkLamp(t, &f, api.LampPause, 0, 0, "PAUSE")
		pattern = pattern<<1 | pattern>>15
	}
}

func TestAddressSources(t *testing.T) {
	const pa, va = 0o17654322, 0o154322
	samples := []api.Sample{busSample(pa, 0, va, 10, 0)}
	tests := []struct {
		name     string
		knob     int
		consAddr uint32
		want     uint32
	}{
		{"PROG PHY shows the 22-bit physical address", api.AddrProgPhy, 0, pa},
		{"CONS PHY: virtual address plus the latched bits 21:16", api.AddrConsPhy, 0o3001234, va | 0o3000000},
		{"KERNEL D: the virtual address, bits 21:16 dark", api.AddrKernelD, 0o3001234, va},
		{"USER I", api.AddrUserI, 0, va},
		{"SUPER I", api.AddrSuperI, 0, va},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e lampEngine
			fs := runningState(tt.knob, api.DataDataPaths)
			fs.cons.addr = tt.consAddr
			var f [api.LampCount]byte
			e.compute(fs, samples, api.PanelSnapshot{}, true, &f)
			if got := field(&f, api.LampA0, 22, 255); got != tt.want {
				t.Errorf("ADDRESS = %o, want %o", got, tt.want)
			}
			if got := field(&f, api.LampA0, 22, 1); got != tt.want {
				t.Errorf("ADDRESS (any light) = %o, want %o", got, tt.want)
			}
		})
	}
}

func TestDataFunctions(t *testing.T) {
	snap := api.PanelSnapshot{DR: 0o52525, LastData: 0o1717}
	instr := busSample(0o2000, 0o123456, 0o2000, 100, 0)
	instr.PC, instr.IR = 0o1000, 0o1003 // (pc^ir)%5 = 3
	wait := waitSample(0o3000, 0o7, 0o3000, 100, 0)
	tests := []struct {
		name    string
		knob    int
		samples []api.Sample
		want    uint16
	}{
		{"DATA PATHS: bus data", 1, []api.Sample{instr}, 0o123456},
		{"DATA PATHS: WAIT samples carry R0", 5, []api.Sample{wait}, 0o7},
		{"BUS REG: bus data", 0, []api.Sample{instr}, 0o123456},
		{"BUS REG: WAIT samples use the snapshot's last bus data", 4, []api.Sample{wait}, 0o1717},
		{"µADRS: running [DEV]", 2, []api.Sample{instr}, uAdrsRunning[3]},
		{"µADRS: WAIT [DEV]", 6, []api.Sample{wait}, uAdrsWait},
		{"DISPLAY REGISTER: the snapshot's DR", 3, []api.Sample{instr, wait}, 0o52525},
		{"DISPLAY REGISTER: also without samples", 7, nil, 0o52525},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e lampEngine
			var f [api.LampCount]byte
			e.compute(runningState(0, tt.knob), tt.samples, snap, true, &f)
			if got := field(&f, api.LampD0, 16, 255); got != uint32(tt.want) {
				t.Errorf("DATA = %06o, want %06o", got, tt.want)
			}
			if got := field(&f, api.LampD0, 16, 1); got != uint32(tt.want) {
				t.Errorf("DATA (any light) = %06o, want %06o", got, tt.want)
			}
		})
	}
}

// drSnap is a snapshot whose DISPLAY REGISTER accumulators hold on[i] µs
// for bit i out of sum (the latch itself shows latch).
func drSnap(latch uint16, sum uint32, on map[int]uint32) api.PanelSnapshot {
	s := api.PanelSnapshot{DR: latch, WeightSum: sum}
	for i, v := range on {
		s.DROn[i] = v
	}
	return s
}

func TestDisplayRegisterTimeWeighted(t *testing.T) {
	var e lampEngine
	var f [api.LampCount]byte
	fs := runningState(api.AddrProgPhy, api.DataDisplayReg)
	s := []api.Sample{busSample(0o1000, 0, 0o1000, 100, 0)}
	dr := func() [16]byte {
		var d [16]byte
		copy(d[:], f[api.LampD0:api.LampD0+16])
		return d
	}
	// The first frame has no baseline: the latch.
	sum0, on0 := uint32(0xFFFFF000), uint32(0xFFFFFF00)
	base := drSnap(0o100001, sum0, map[int]uint32{0: on0, 3: 7, 15: 0})
	e.compute(fs, s, base, true, &f)
	if got := field(&f, api.LampD0, 16, 1); got != 0o100001 {
		t.Fatalf("first frame: DR lamps %06o, want the latch 100001", got)
	}
	// 1000 µs later (the accumulators wrap at 2^32): bit 0 was on half the
	// time, bit 3 a quarter, bit 15 all of it; the latch does not matter.
	next := drSnap(0o000002, sum0+1000, map[int]uint32{0: on0 + 500, 3: 7 + 250, 15: 1000})
	e.compute(fs, s, next, true, &f)
	want := [16]byte{0: 128, 3: 64, 15: 255}
	if got := dr(); got != want {
		t.Fatalf("time-weighted DR = %v, want %v", got, want)
	}
	// A frame without samples (the accumulators did not move): the latch.
	e.compute(fs, nil, next, true, &f)
	if got := field(&f, api.LampD0, 16, 1); got != 0o2 || f[api.LampD0+1] != 255 {
		t.Fatalf("frame without samples: DR lamps %06o, want the latch 000002", got)
	}
	// Halted: the latch (§4.2), and the baseline follows.
	halted := *fs
	halted.run = api.RunHalted
	stopped := drSnap(0o40, next.WeightSum+300, map[int]uint32{0: next.DROn[0] + 300, 3: next.DROn[3], 15: 1000})
	e.compute(&halted, nil, stopped, true, &f)
	if got := field(&f, api.LampD0, 16, 1); got != 0o40 {
		t.Fatalf("halted: DR lamps %06o, want the latch 000040", got)
	}
	resumed := stopped
	resumed.WeightSum += 100
	resumed.DROn[5] += 100
	e.compute(fs, s, resumed, true, &f)
	if got := dr(); got != ([16]byte{5: 255}) {
		t.Fatalf("after halted frames: DR = %v, want bit 5 only", got)
	}
	// A power cycle starts a new baseline (the counters of a new export).
	e.compute(&frameState{key: keyPower, run: api.RunStarting}, nil, api.PanelSnapshot{}, false, &f)
	e.compute(fs, s, drSnap(0o7, 5, map[int]uint32{0: 5}), true, &f)
	if got := field(&f, api.LampD0, 16, 1); got != 0o7 {
		t.Fatalf("after a power cycle: DR lamps %06o, want the latch 000007", got)
	}
}

func TestStatusLampDuty(t *testing.T) {
	user, super, kernel := modeBits(api.ModeUser), modeBits(api.ModeSuper), modeBits(api.ModeKernel)
	tests := []struct {
		name    string
		samples []api.Sample
		want    map[int]byte // lamp -> duty
	}{
		{
			name: "modes and D space are weighted by wall time",
			samples: []api.Sample{
				busSample(0o1000, 0, 0, 500, user|api.PnlDSpace),
				busSample(0o1000, 0, 0, 250, super),
				busSample(0o1000, 0, 0, 250, kernel|api.PnlDSpace),
				busSample(0o1000, 0, 0, 0, modeBits(2)), // weight 0 counts as 1; mode 2 lights nothing
			},
			want: map[int]byte{api.LampUser: 127, api.LampSuper: 64, api.LampKernel: 64, api.LampData: 191},
		},
		{
			name: "ADDRESSING 16/18/22",
			samples: []api.Sample{
				busSample(0, 0, 0, 200, 0),
				busSample(0, 0, 0, 300, api.PnlMMU),
				busSample(0, 0, 0, 500, api.PnlMMU|api.PnlM22),
				busSample(0, 0, 0, 0, api.PnlM22), // M22 without MMU is 16-bit
			},
			want: map[int]byte{api.LampAdrs16: 51, api.LampAdrs18: 76, api.LampAdrs22: 127},
		},
		{
			name:    "RUN is 0.92 while executing",
			samples: []api.Sample{busSample(0o1000, 0, 0, 100, 0)},
			want:    map[int]byte{api.LampRun: 235, api.LampMaster: 0, api.LampPause: 0},
		},
		{
			name:    "RUN is fully on in WAIT; MASTER off",
			samples: []api.Sample{waitSample(0o1000, 0, 0, 100, 0)},
			want:    map[int]byte{api.LampRun: 255, api.LampMaster: 0, api.LampPause: 0},
		},
		{
			name: "MASTER follows I/O-page cycles, PAUSE half of it",
			samples: []api.Sample{
				busSample(0o17777546, 0, 0o177546, 500, api.PnlIOPage),
				busSample(0o1000, 0, 0o1000, 500, 0),
				waitSample(0o17777546, 0, 0o1000, 0, api.PnlIOPage), // WAIT samples are not bus master
			},
			want: map[int]byte{api.LampMaster: 127, api.LampPause: 64, api.LampRun: 234},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e lampEngine
			var f [api.LampCount]byte
			e.compute(runningState(0, 1), tt.samples, api.PanelSnapshot{}, true, &f)
			for lamp, want := range tt.want {
				checkLamp(t, &f, lamp, want, 1, "lamp")
			}
		})
	}
}

func TestParity(t *testing.T) {
	read := func(data uint16, flags uint32) api.Sample { return busSample(0o1000, data, 0o1000, 10, flags) }
	tests := []struct {
		name   string
		frames [][]api.Sample
		hi, lo bool // of the last frame
	}{
		{"word read: lamp on for a byte with an even number of ones", [][]api.Sample{{read(0x0301, 0)}}, true, false},
		{"word read: both even", [][]api.Sample{{read(0x0000, 0)}}, true, true},
		{"word read: both odd", [][]api.Sample{{read(0x0107, 0)}}, false, false},
		{"writes clear both", [][]api.Sample{{read(0, 0)}, {read(0, api.PnlWR)}}, false, false},
		{"I/O-page reads clear both", [][]api.Sample{{read(0, 0)}, {read(0, api.PnlIOPage)}}, false, false},
		{"byte read at an even address: low lane only", [][]api.Sample{{busSample(0o1000, 0x03, 0, 10, api.PnlByte)}}, false, true},
		{"byte read at an odd address: high lane only", [][]api.Sample{{busSample(0o1001, 0x03, 0, 10, api.PnlByte)}}, true, false},
		{"WAIT samples repeat the previous non-WAIT parity (across frames)",
			[][]api.Sample{{read(0x0301, 0)}, {waitSample(0o1000, 0x0101, 0, 10, 0)}}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e lampEngine
			var f [api.LampCount]byte
			for _, s := range tt.frames {
				e.compute(runningState(0, 1), s, api.PanelSnapshot{}, true, &f)
			}
			if got := f[api.LampParityHigh] == 255; got != tt.hi {
				t.Errorf("PARITY HIGH = %d, want on=%v", f[api.LampParityHigh], tt.hi)
			}
			if got := f[api.LampParityLow] == 255; got != tt.lo {
				t.Errorf("PARITY LOW = %d, want on=%v", f[api.LampParityLow], tt.lo)
			}
			checkLamp(t, &f, api.LampParErr, 0, 0, "PAR ERR")
		})
	}
}

func TestAdrsErrFlashesOnTrap(t *testing.T) {
	var e lampEngine
	var f [api.LampCount]byte
	fs := runningState(0, 1)
	s := []api.Sample{busSample(0o1000, 0, 0, 10, 0)}
	steps := []struct {
		traps uint32
		samp  []api.Sample
		want  byte
	}{
		{5, s, 0},     // first powered frame: baseline, no flash
		{5, s, 0},     // unchanged
		{6, s, 255},   // a trap since the previous frame
		{6, s, 0},     // one frame only
		{9, nil, 255}, // also in a frame without samples
		{9, nil, 0},   // and not repeated from the previous frame
	}
	for i, st := range steps {
		e.compute(fs, st.samp, api.PanelSnapshot{TrapCount: st.traps}, true, &f)
		if f[api.LampAdrsErr] != st.want {
			t.Fatalf("frame %d (traps %d): ADRS ERR = %d, want %d", i, st.traps, f[api.LampAdrsErr], st.want)
		}
	}
	// A power cycle starts a new baseline (the new simulator counts from 0).
	e.compute(&frameState{key: keyPower, run: api.RunStarting}, nil, api.PanelSnapshot{}, false, &f)
	e.compute(fs, s, api.PanelSnapshot{TrapCount: 0}, true, &f)
	if f[api.LampAdrsErr] != 0 {
		t.Fatal("ADRS ERR flashed after a power cycle")
	}
}

func TestFrameWithoutSamplesRepeatsPrevious(t *testing.T) {
	var e lampEngine
	var f1, f2 [api.LampCount]byte
	fs := runningState(api.AddrProgPhy, api.DataDataPaths)
	e.compute(fs, []api.Sample{busSample(0o1234, 0o4321, 0o1234, 10, api.PnlMMU)}, api.PanelSnapshot{DR: 1}, true, &f1)
	e.compute(fs, nil, api.PanelSnapshot{DR: 2}, true, &f2)
	if f1 != f2 {
		t.Fatalf("frame without samples changed:\n%v\n%v", f1, f2)
	}
	// DISPLAY REGISTER follows the snapshot even then.
	fs.dataFunc = api.DataDisplayReg
	e.compute(fs, nil, api.PanelSnapshot{DR: 0o707}, true, &f2)
	if got := field(&f2, api.LampD0, 16, 255); got != 0o707 {
		t.Fatalf("DR = %o", got)
	}
	if got := field(&f2, api.LampA0, 22, 255); got != 0o1234 {
		t.Fatalf("ADDRESS not repeated: %o", got)
	}
	// The knob lamps follow the knobs.
	if f2[api.LampDisplayReg] != 255 || f2[api.LampDataPaths] != 0 {
		t.Fatal("knob lamps not updated in a repeated frame")
	}
}

func TestHaltedDisplay(t *testing.T) {
	snap := api.PanelSnapshot{DR: 0o1717, LastPA: 0o3456 | api.PnlWR, PSW: 0o140000, MMR0: 1, MMR3: 1 << 4}
	tests := []struct {
		name     string
		knob     int
		dataKnob int
		cons     console
		sr       uint32
		snap     api.PanelSnapshot
		addr     uint32
		data     uint16
		lamps    map[int]byte
	}{
		{name: "general register: forced pattern in a virtual position", knob: api.AddrUserD,
			cons: console{addr: 0o17777703}, addr: 0o17777703, dataKnob: 1},
		{name: "general register: forced pattern in PROG PHY", knob: api.AddrProgPhy,
			cons: console{addr: 0o17777717, consPA: 0o1000, consPAOK: true}, addr: 0o17777717, dataKnob: 1},
		{name: "virtual positions show bits 15:0", knob: api.AddrKernelI,
			cons: console{addr: 0o3001234}, addr: 0o1234, dataKnob: 1},
		{name: "CONS PHY shows all 22 bits", knob: api.AddrConsPhy,
			cons: console{addr: 0o3001234}, addr: 0o3001234, dataKnob: 1},
		{name: "PROG PHY shows the PA of the last console access", knob: api.AddrProgPhy,
			cons: console{addr: 0o1234, consPA: 0o201234, consPAOK: true}, addr: 0o201234, dataKnob: 1},
		{name: "PROG PHY otherwise shows the program's last bus address", knob: api.AddrProgPhy,
			cons: console{addr: 0o1234}, addr: 0o3456, dataKnob: 1},
		{name: "DATA PATHS", knob: api.AddrConsPhy, dataKnob: 1, cons: console{dataPaths: 0o177001}, data: 0o177001},
		{name: "BUS REG shows the live switch register", knob: api.AddrConsPhy, dataKnob: 0, sr: 0o7654321, data: 0o54321},
		{name: "µADRS shows CON.00", knob: api.AddrConsPhy, dataKnob: 2, data: 0o170},
		{name: "DISPLAY REGISTER", knob: api.AddrConsPhy, dataKnob: 7, data: 0o1717},
		{name: "status lamps: MASTER, user mode, 22-bit", knob: 0, dataKnob: 1,
			lamps: map[int]byte{api.LampMaster: 255, api.LampRun: 0, api.LampPause: 0, api.LampUser: 255,
				api.LampKernel: 0, api.LampAdrs22: 255, api.LampAdrs16: 0, api.LampAdrsErr: 0, api.LampData: 0,
				api.LampParityHigh: 0, api.LampParityLow: 0}},
		{name: "status lamps: kernel, 18-bit, static ADRS ERR", knob: 0, dataKnob: 1, cons: console{adrsErr: true},
			snap:  api.PanelSnapshot{PSW: 0, MMR0: 1, MMR3: 0},
			lamps: map[int]byte{api.LampKernel: 255, api.LampAdrs18: 255, api.LampAdrsErr: 255, api.LampUser: 0}},
		{name: "status lamps: supervisor, 16-bit", knob: 0, dataKnob: 1,
			snap:  api.PanelSnapshot{PSW: 0o040000, MMR3: 1 << 4},
			lamps: map[int]byte{api.LampSuper: 255, api.LampAdrs16: 255, api.LampAdrs22: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var e lampEngine
			var f [api.LampCount]byte
			s := snap
			if tt.snap != (api.PanelSnapshot{}) {
				s = tt.snap
			}
			fs := &frameState{key: keyPower, run: api.RunHalted, addrKnob: tt.knob, dataFunc: tt.dataKnob % 4, sr: tt.sr, cons: tt.cons}
			// Samples left over from the run do not matter while halted.
			e.compute(fs, []api.Sample{busSample(0o7777, 0o7777, 0o7777, 10, 0)}, s, true, &f)
			if tt.lamps == nil {
				if got := field(&f, api.LampA0, 22, 255); got != tt.addr {
					t.Errorf("ADDRESS = %o, want %o", got, tt.addr)
				}
				if got := field(&f, api.LampD0, 16, 255); got != uint32(tt.data) {
					t.Errorf("DATA = %o, want %o", got, tt.data)
				}
			}
			for lamp, want := range tt.lamps {
				checkLamp(t, &f, lamp, want, 0, "lamp")
			}
			for i := 0; i < api.LampCount; i++ {
				if f[i] != 0 && f[i] != 255 {
					t.Errorf("halted lamp %d = %d (halted lamps are on or off)", i, f[i])
				}
			}
		})
	}
}

func TestPowerLampTestAndKnobLamps(t *testing.T) {
	s := []api.Sample{busSample(0o1000, 0o1, 0o1000, 10, 0)}
	t.Run("key off: all dark, even with LAMP TEST", func(t *testing.T) {
		var e lampEngine
		var f [api.LampCount]byte
		e.compute(&frameState{key: keyOff, run: api.RunRunning, lampTest: true}, s, api.PanelSnapshot{}, true, &f)
		if f != ([api.LampCount]byte{}) {
			t.Fatalf("lamps lit with the key at OFF: %v", f)
		}
	})
	for _, st := range []api.RunState{api.RunOff, api.RunStarting, api.RunStopping, api.RunError} {
		t.Run("machine "+string(st)+": CPU section dark, knob lamps lit, LAMP TEST lights everything", func(t *testing.T) {
			var e lampEngine
			var f [api.LampCount]byte
			for _, key := range []string{keyPower, keyLock} {
				e.compute(&frameState{key: key, run: st, addrKnob: api.AddrKernelD, dataFunc: api.DataUAdrs},
					s, api.PanelSnapshot{DR: 0o177777}, true, &f)
				for i, v := range f {
					want := byte(0)
					if i == api.LampKernelD || i == api.LampUAdrs {
						want = 255
					}
					if v != want {
						t.Fatalf("key %s: lamp %d = %d, want %d", key, i, v, want)
					}
				}
			}
			e.compute(&frameState{key: keyLock, run: st, lampTest: true}, s, api.PanelSnapshot{}, true, &f)
			for i, v := range f {
				if v != 255 {
					t.Fatalf("lamp %d = %d during LAMP TEST", i, v)
				}
			}
		})
	}
	t.Run("LAMP TEST while running", func(t *testing.T) {
		var e lampEngine
		var f [api.LampCount]byte
		fs := runningState(0, 1)
		fs.lampTest = true
		e.compute(fs, s, api.PanelSnapshot{}, true, &f)
		for i, v := range f {
			if v != 255 {
				t.Fatalf("lamp %d = %d during LAMP TEST", i, v)
			}
		}
		// The engine kept computing underneath: releasing shows real lamps at once.
		fs.lampTest = false
		e.compute(fs, nil, api.PanelSnapshot{}, true, &f)
		if f[api.LampA0+9] != 255 || f[api.LampA0+8] != 0 {
			t.Fatalf("after LAMP TEST: ADDRESS lamps %v", f[:22])
		}
	})
	t.Run("knob lamps", func(t *testing.T) {
		addrLamp := []int{api.LampProgPhy, api.LampConsPhy, api.LampKernelD, api.LampSuperD, api.LampUserD,
			api.LampUserI, api.LampSuperI, api.LampKernelI}
		dataLamp := []int{api.LampBusReg, api.LampDataPaths, api.LampUAdrs, api.LampDisplayReg}
		for _, run := range []api.RunState{api.RunRunning, api.RunHalted, api.RunOff, api.RunStarting, api.RunError} {
			for a := 0; a < 8; a++ {
				for d := 0; d < 8; d++ {
					var e lampEngine
					var f [api.LampCount]byte
					e.compute(&frameState{key: keyPower, run: run, addrKnob: a, dataFunc: d % 4}, s, api.PanelSnapshot{}, true, &f)
					for i := api.LampUserD; i <= api.LampDisplayReg; i++ {
						want := byte(0)
						if i == addrLamp[a] || i == dataLamp[d%4] {
							want = 255
						}
						if f[i] != want {
							t.Fatalf("%s addr %d data %d: knob lamp %d = %d", run, a, d, i, f[i])
						}
					}
				}
			}
		}
	})
}

// TestFrameDelivery runs the frame loop: frames go out when lamps change
// and at least once per keepalive period; DATA PATHS of a halted machine
// shows R0 after a stop.
func TestFrameDelivery(t *testing.T) {
	old := frameKeepalive
	frameKeepalive = 150 * time.Millisecond
	t.Cleanup(func() { frameKeepalive = old })

	e := runningEnv(t)
	type rec struct {
		f   [api.LampCount]byte
		seq uint16
		at  time.Time
	}
	ch := make(chan rec, 1024)
	cancel := e.p.SubscribeFrames(func(f *[api.LampCount]byte, seq uint16) {
		select {
		case ch <- rec{*f, seq, time.Now()}:
		default:
		}
	})
	defer cancel()
	next := func() rec {
		t.Helper()
		select {
		case r := <-ch:
			return r
		case <-time.After(5 * time.Second):
			t.Fatal("no frame")
		}
		return rec{}
	}
	// Steady samples: after the first frame, only keepalives.
	e.src.Push(busSample(0o1234, 0o4321, 0o1234, 10, 0))
	first := next()
	for first.f[api.LampA0+2] != 255 { // wait for the frame with the sample
		first = next()
	}
	second := next()
	if second.seq != first.seq+1 {
		t.Fatalf("seq %d after %d", second.seq, first.seq)
	}
	if gap := second.at.Sub(first.at); gap < 100*time.Millisecond {
		t.Fatalf("unchanged frame sent after %v (keepalive is 150 ms)", gap)
	}
	// A change goes out at once.
	e.src.Push(busSample(0o7, 0, 0o7, 10, 0))
	r := next()
	if r.f[api.LampA0] != 255 || r.f[api.LampA0+2] != 255 || r.f[api.LampA0+3] != 0 {
		t.Fatalf("frame after change: %v", r.f[:22])
	}

	// Halt: DATA PATHS shows R0 of the snapshot taken after the stop.
	e.src.SetSnapshot(api.PanelSnapshot{R: [8]uint16{0o1357}})
	e.do("halt")
	if !paneltest.WaitFor(5*time.Second, func() bool {
		r := next()
		return field(&r.f, api.LampD0, 16, 255) == 0o1357 && r.f[api.LampMaster] == 255
	}) {
		t.Fatal("halted frame does not show R0 in DATA PATHS")
	}
}

func TestDutyRounding(t *testing.T) {
	for _, tt := range []struct {
		on, total float64
		want      byte
	}{{0, 10, 0}, {10, 10, 255}, {5, 10, 128}, {1, 1e6, 0}, {1e6, 1e6, 255}, {20, 10, 255}} {
		if got := duty(tt.on, tt.total); got != tt.want {
			t.Errorf("duty(%v, %v) = %d, want %d", tt.on, tt.total, got, tt.want)
		}
	}
	// addBits/setBits agree with the bit positions.
	var acc [api.LampCount]float64
	addBits(&acc, api.LampD0, 0x8001, 2)
	if acc[api.LampD0] != 2 || acc[api.LampD0+15] != 2 || bits.OnesCount32(0x8001) != 2 {
		t.Fatal("addBits")
	}
}
