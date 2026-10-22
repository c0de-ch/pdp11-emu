package panel

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel/paneltest"
)

func TestStep(t *testing.T) {
	tests := []struct {
		a, want uint32
		ok      bool
	}{
		{01000, 01002, true},
		{01001, 01003, true},                 // odd addresses step by 2 as well
		{0177776, 0, true},                   // wraps within the first 64 KB block
		{0377776, 0200000, true},             // ... and within any other block
		{017777776, 017600000, true},         // I/O page: still within its block
		{017777674, 017777676, true},         // up to the register boundary
		{017777676, 0, false},                // cannot step into the registers
		{017777700, 017777701, true},         // registers step by 1
		{017777707, 017777710, true},         // PC -> R10
		{017777717, 017777700, true},         // USP wraps to R00
		{grFirst + 015, grFirst + 016, true}, // R15 -> SSP
		{017600000 | 0123456, 017600000 | 0123460, true},
	}
	for _, tt := range tests {
		got, ok := step(tt.a)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("step(%o) = %o, %v; want %o, %v", tt.a, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLocate(t *testing.T) {
	tests := []struct {
		a    uint32
		knob int
		want string
	}{
		{017777700, api.AddrProgPhy, "R00"},
		{017777705, api.AddrUserD, "R05"}, // registers regardless of the knob
		{017777706, api.AddrKernelI, "KSP"},
		{017777707, api.AddrConsPhy, "PC"},
		{017777710, api.AddrSuperD, "R10"},
		{017777715, api.AddrUserI, "R15"},
		{017777716, api.AddrKernelD, "SSP"},
		{017777717, api.AddrSuperI, "USP"},
		{01001, api.AddrProgPhy, "1000"}, // physical: bit 0 ignored
		{01001, api.AddrConsPhy, "1000"},
		{017777570, api.AddrConsPhy, "17777570"},
		{0201235, api.AddrKernelD, "KD:1234"}, // virtual: 16 bits, even
		{0201234, api.AddrSuperD, "SD:1234"},
		{0201234, api.AddrUserD, "UD:1234"},
		{0201234, api.AddrUserI, "UI:1234"},
		{0201234, api.AddrSuperI, "SI:1234"},
		{0201234, api.AddrKernelI, "KI:1234"},
		{017777570, api.AddrKernelI, "KI:177570"},
	}
	for _, tt := range tests {
		if got := fmtLoc(locate(tt.a, tt.knob)); got != tt.want {
			t.Errorf("locate(%o, %d) = %s, want %s", tt.a, tt.knob, got, tt.want)
		}
	}
}

// TestConsoleSequences drives the console switches through the real panel
// and a fake machine and checks the Machine operations and the console
// state. Actions: see env.do.
func TestConsoleSequences(t *testing.T) {
	type want struct {
		ops       []string // console-relevant Machine calls
		addr      uint32
		adrsErr   bool
		dataPaths *uint16
		hints     []string // substrings of the hints returned by the actions, in order
		consPA    *uint32  // nil: not checked; ^0: no consPA
	}
	u16 := func(v uint16) *uint16 { return &v }
	u32 := func(v uint32) *uint32 { return &v }
	none := u32(^uint32(0))
	tests := []struct {
		name    string
		setup   func(m *paneltest.Machine)
		actions []string
		want    want
	}{
		{
			name:    "examine steps on repeat",
			setup:   func(m *paneltest.Machine) { m.SetMem(01004, 0123) },
			actions: []string{"sr 1000", "load", "exam", "exam", "exam"},
			want: want{ops: []string{"Examine 1000", "Examine 1002", "Examine 1004"}, addr: 01004,
				dataPaths: u16(0123), consPA: u32(01004)},
		},
		{
			// HB 11.5: LOAD ADRS→X, EXAM→X, DEP→X, EXAM→X, EXAM→X+2, DEP→X+2, EXAM→X+2.
			name:    "DEC exam/deposit sequence",
			actions: []string{"sr 2000", "load", "exam", "sr 111", "dep", "exam", "exam", "sr 222", "dep", "exam"},
			want: want{ops: []string{"Examine 2000", "Deposit 2000=111", "Examine 2000", "Examine 2002",
				"Deposit 2002=222", "Examine 2002"}, addr: 02002, dataPaths: u16(0222)},
		},
		{
			name:    "deposit steps on repeat",
			actions: []string{"sr 3000", "load", "sr 7", "dep", "dep", "dep"},
			want: want{ops: []string{"Deposit 3000=7", "Deposit 3002=7", "Deposit 3004=7"}, addr: 03004,
				dataPaths: u16(7)},
		},
		{
			name:    "load address ends a step sequence and clears data paths",
			setup:   func(m *paneltest.Machine) { m.SetMem(01000, 0777) },
			actions: []string{"sr 1000", "load", "exam", "exam", "load"},
			want:    want{ops: []string{"Examine 1000", "Examine 1002"}, addr: 01000, dataPaths: u16(0)},
		},
		{
			name:  "general registers regardless of the knob, stepping by one and wrapping",
			setup: func(m *paneltest.Machine) { m.SetReg("USP", 0456); m.SetReg("R00", 0123) },
			actions: append([]string{"addr 4", "sr 17777700", "load"},
				slices.Repeat([]string{"exam"}, 17)...),
			want: want{ops: []string{"Examine R00", "Examine R01", "Examine R02", "Examine R03", "Examine R04",
				"Examine R05", "Examine KSP", "Examine PC", "Examine R10", "Examine R11", "Examine R12",
				"Examine R13", "Examine R14", "Examine R15", "Examine SSP", "Examine USP", "Examine R00"},
				addr: 017777700, dataPaths: u16(0123), consPA: none},
		},
		{
			name:    "deposit into a register",
			actions: []string{"sr 17777707", "load", "sr 1000", "dep"},
			want:    want{ops: []string{"Deposit PC=1000"}, addr: 017777707, dataPaths: u16(01000)},
		},
		{
			name:    "cannot step from 17777676 into the registers",
			setup:   func(m *paneltest.Machine) { m.SetIOPage(017777676, 0) },
			actions: []string{"sr 17777676", "load", "exam", "sr 1234", "exam"},
			want:    want{ops: []string{"Examine 17777676"}, addr: 01234, adrsErr: true},
		},
		{
			// LCM 2018: EXAM of non-existent memory clears ADDRESS<15:0>.
			name:    "non-existent memory: ADRS ERR, address bits 15:0 cleared",
			actions: []string{"sr 2001234", "load", "exam"},
			want:    want{ops: []string{"Examine 2001234"}, addr: 02000000, adrsErr: true, consPA: none},
		},
		{
			name:    "deposit into non-existent memory",
			actions: []string{"sr 2001234", "load", "dep"},
			want:    want{ops: []string{"Deposit 2001234=1234"}, addr: 02000000, adrsErr: true},
		},
		{
			name:    "error while stepping keeps the stepped address",
			setup:   func(m *paneltest.Machine) { m.Lock(); m.MemSize = 0100000; m.Unlock() },
			actions: []string{"sr 77776", "load", "exam", "exam"},
			want:    want{ops: []string{"Examine 77776", "Examine 100000"}, addr: 0100000, adrsErr: true},
		},
		{
			name:    "EXAM and DEP are ignored after ADRS ERR until LOAD ADRS",
			actions: []string{"sr 2001234", "load", "exam", "exam", "dep", "sr 1000", "load", "exam"},
			want: want{ops: []string{"Examine 2001234", "Examine 1000"}, addr: 01000,
				hints: []string{"ADRS ERR", "ADRS ERR"}},
		},
		{
			name: "virtual examine uses D space (-t), translated for PROG PHY",
			setup: func(m *paneltest.Machine) {
				m.MMU = func(l api.Location) (uint32, error) { return 0200000 + l.Addr, nil }
			},
			actions: []string{"addr 2", "sr 1234", "load", "exam"},
			want:    want{ops: []string{"Examine KD:1234", "Translate KD:1234"}, addr: 01234, consPA: u32(0201234)},
		},
		{
			name: "virtual user I space",
			setup: func(m *paneltest.Machine) {
				m.MMU = func(l api.Location) (uint32, error) { return 0400000 + l.Addr, nil }
			},
			actions: []string{"addr 5", "sr 4001234", "load", "dep"},
			want:    want{ops: []string{"Deposit UI:1234=1234", "Translate UI:1234"}, addr: 04001234, consPA: u32(0401234)},
		},
		{
			name: "MMU abort on a virtual address is an ADRS ERR",
			setup: func(m *paneltest.Machine) {
				m.MMU = func(l api.Location) (uint32, error) { return 0, fmt.Errorf("%w: Relocation error", api.ErrAddress) }
			},
			actions: []string{"addr 7", "sr 1234", "load", "exam"},
			want:    want{ops: []string{"Examine KI:1234"}, addr: 0, adrsErr: true},
		},
		{
			name:    "S BUS CYCLE: LOAD ADRS, EXAM and DEP do nothing",
			actions: []string{"sr 1000", "load", "sbus", "sr 2000", "load", "exam", "dep"},
			want:    want{ops: nil, addr: 01000, hints: []string{"S BUS", "S BUS", "S BUS"}},
		},
		{
			name:    "CONT with HALT steps one instruction; the address follows the PC",
			setup:   func(m *paneltest.Machine) { m.SetPC(01000) },
			actions: []string{"sr 1000", "load", "exam", "cont"},
			want:    want{ops: []string{"Examine 1000", "Step"}, addr: 01002, consPA: none},
		},
		{
			name:    "CONT in S BUS CYCLE steps a whole instruction [DEV]",
			setup:   func(m *paneltest.Machine) { m.SetPC(02000) },
			actions: []string{"sbus", "cont", "cont"},
			want:    want{ops: []string{"Step", "Step"}, addr: 02004},
		},
		{
			name:    "a stop keeps address bits 21:16",
			setup:   func(m *paneltest.Machine) { m.SetPC(01000) },
			actions: []string{"sr 3000000", "load", "cont"},
			want:    want{ops: []string{"Step"}, addr: 03001002},
		},
		{
			name:    "CONT with ENABLE continues",
			actions: []string{"enable", "cont"},
			want:    want{ops: []string{"Continue"}},
		},
		{
			name:    "START with HALT is a console reset",
			actions: []string{"sr 1000", "load", "start"},
			want:    want{ops: []string{"ConsoleReset"}, addr: 01000},
		},
		{
			name:    "START with ENABLE starts at the console address (16 bits)",
			actions: []string{"sr 3001000", "load", "enable", "start"},
			want:    want{ops: []string{"Start 1000"}, addr: 03001000},
		},
		{
			name:    "START at the boot ROM 165000 boots [DEV]",
			actions: []string{"sr 165000", "load", "enable", "start"},
			want:    want{ops: []string{"Boot"}, addr: 0165000}, // a fresh simulator boots without a restart
		},
		{
			name:    "START at the boot ROM 173000 boots [DEV]",
			actions: []string{"sr 17773000", "load", "enable", "start"},
			want:    want{ops: []string{"Boot"}, addr: 017773000},
		},
		{
			name:    "console switches need a halted CPU",
			actions: []string{"enable", "cont", "sr 1000", "load", "exam", "dep", "start", "cont"},
			want: want{ops: []string{"Continue"}, hints: []string{"only when the CPU is halted",
				"only when the CPU is halted", "only when the CPU is halted", "only when the CPU is halted", "only when the CPU is halted"}},
		},
		{
			name:    "LOCK disables the console switches but not SR or the knobs",
			actions: []string{"key lock", "sr 1000", "load", "exam", "cont", "addr 3"},
			want:    want{ops: nil, hints: []string{"LOCK", "LOCK", "LOCK"}},
		},
		{
			name:    "CONT clears a static ADRS ERR",
			actions: []string{"sr 2001234", "load", "exam", "cont"},
			want:    want{ops: []string{"Examine 2001234", "Step"}, adrsErr: false, addr: 02000106},
		},
		{
			name: "another viewer resumed the CPU: ignored, no ADRS ERR, no step",
			setup: func(m *paneltest.Machine) {
				m.BeforeOp = func(name string) {
					if name == "Examine" && m.Count("Examine") == 1 {
						m.SetState(api.RunRunning)
					}
				}
			},
			actions: []string{"enable", "sr 1000", "load", "exam", "exam"},
			want:    want{ops: []string{"Examine 1000", "Examine 1002"}, addr: 01000, hints: []string{"only when the CPU is halted"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := haltedEnv(t)
			if tt.setup != nil {
				tt.setup(e.m)
			}
			var hints []string
			for _, a := range tt.actions {
				if h := e.do(a); h != "" {
					hints = append(hints, h)
				}
			}
			for _, h := range e.hintList() { // asynchronous hints of queued actions
				hints = append(hints, h)
			}
			if got := e.ops(); !slices.Equal(got, tt.want.ops) {
				t.Errorf("ops = %q\nwant %q", got, tt.want.ops)
			}
			c := e.cons()
			if c.addr != tt.want.addr {
				t.Errorf("console address = %o, want %o", c.addr, tt.want.addr)
			}
			if c.adrsErr != tt.want.adrsErr {
				t.Errorf("adrsErr = %v, want %v", c.adrsErr, tt.want.adrsErr)
			}
			if tt.want.dataPaths != nil && c.dataPaths != *tt.want.dataPaths {
				t.Errorf("dataPaths = %o, want %o", c.dataPaths, *tt.want.dataPaths)
			}
			if tt.want.consPA != nil {
				if *tt.want.consPA == ^uint32(0) {
					if c.consPAOK {
						t.Errorf("consPA = %o, want none", c.consPA)
					}
				} else if !c.consPAOK || c.consPA != *tt.want.consPA {
					t.Errorf("consPA = %o (%v), want %o", c.consPA, c.consPAOK, *tt.want.consPA)
				}
			}
			if len(hints) != len(tt.want.hints) {
				t.Fatalf("hints = %q, want %d like %q", hints, len(tt.want.hints), tt.want.hints)
			}
			for i, h := range hints {
				if !strings.Contains(h, tt.want.hints[i]) {
					t.Errorf("hint %d = %q, want it to contain %q", i, h, tt.want.hints[i])
				}
			}
			if got := e.p.Console(); got.Addr != c.addr || got.AdrsErr != c.adrsErr {
				t.Errorf("Console() = %+v, inconsistent with %+v", got, c)
			}
		})
	}
}

func TestStopUpdatesConsole(t *testing.T) {
	e := runningEnv(t)
	e.src.SetSnapshot(api.PanelSnapshot{R: [8]uint16{0o4321}})
	e.do("sr 3000000") // SR alone does not change the console address
	e.m.StopCPU("HALT instruction", 02050)
	e.idle()
	c := e.cons()
	if c.addr != 02050 || c.lastStop != "HALT instruction" || c.pc != 02050 || c.lastOp != lastNone {
		t.Fatalf("after stop: %+v", c)
	}
	// DATA PATHS shows R0 after a stop [DEV]; it comes from the snapshot
	// at the next frame.
	if !paneltest.WaitFor(e.timeout(), func() bool { return e.cons().dataPaths == 0o4321 }) {
		t.Fatalf("dataPaths = %o, want R0 4321", e.cons().dataPaths)
	}
	info := e.p.Console()
	if info.LastStop != "HALT instruction" || info.PC != 02050 || info.Addr != 02050 {
		t.Fatalf("Console() = %+v", info)
	}
}

func TestPowerOnInitializesConsole(t *testing.T) {
	e := haltedEnv(t)
	for _, a := range []string{"sr 2001234", "load", "exam"} {
		e.do(a)
	}
	if c := e.cons(); !c.adrsErr {
		t.Fatalf("setup: %+v", c)
	}
	e.do("key off")
	e.do("key power")
	if st := e.m.State(); st != api.RunHalted {
		t.Fatalf("state %s", st)
	}
	c := e.cons()
	if c.addr != 0 || c.adrsErr || c.dataPaths != 0 || c.lastOp != lastNone || c.consPAOK || c.lastStop != "" {
		t.Fatalf("console after power-on: %+v", c)
	}
}

func TestMomentarySwitches(t *testing.T) {
	e := haltedEnv(t)
	press := func(c uint64, name string, active bool) string {
		return e.input(api.PanelInput{Client: c, Kind: "momentary", Name: name, Active: active})
	}
	press(1, swExam, true)
	e.idle()
	press(2, swExam, true) // already down: no second action
	e.idle()
	if n := e.m.Count("Examine"); n != 1 {
		t.Fatalf("Examine called %d times, want 1", n)
	}
	if !e.p.Controls().Pressed[swExam] {
		t.Fatal("EXAM not shown pressed")
	}
	press(1, swExam, false)
	if !e.p.Controls().Pressed[swExam] {
		t.Fatal("EXAM released while client 2 still holds it")
	}
	e.p.ReleaseClient(2)
	if e.p.Controls().Pressed[swExam] {
		t.Fatal("EXAM still pressed after its last holder disconnected")
	}
	press(1, swExam, true) // a new rising edge
	e.idle()
	if n := e.m.Count("Examine"); n != 2 {
		t.Fatalf("Examine called %d times, want 2", n)
	}

	press(3, swLampTest, true)
	if !e.p.Controls().LampTest {
		t.Fatal("lamp test not active")
	}
	e.p.ReleaseClient(3)
	if e.p.Controls().LampTest {
		t.Fatal("lamp test still active after disconnect")
	}
	if h := press(1, "SWITCH", true); h == "" {
		t.Fatal("unknown switch accepted")
	}
	c := e.p.Controls()
	if len(c.Pressed) != len(consoleSwitches) {
		t.Fatalf("Pressed = %v", c.Pressed)
	}
}

func TestQueuedBehindPowerOn(t *testing.T) {
	t.Run("console actions wait for the power-on and then run", func(t *testing.T) {
		c := defaultControls()
		c.halt, c.key = true, keyOff
		e := newEnv(t, withControls(c))
		e.m.Lock()
		e.m.PowerOnDelay = 100 * time.Millisecond
		e.m.Unlock()
		e.m.SetMem(01000, 0o777)
		e.do("sr 1000")
		e.input(api.PanelInput{Kind: "key", Value: keyPower})
		for _, a := range []string{swLoadAdrs, swExam} {
			if h := e.input(api.PanelInput{Kind: "momentary", Name: a, Active: true}); h != "" {
				t.Fatalf("%s while starting: hint %q (should be queued)", a, h)
			}
			e.input(api.PanelInput{Kind: "momentary", Name: a, Active: false})
		}
		e.idle()
		if got := e.ops(); !slices.Equal(got, []string{"PowerOn boot=false", "Examine 1000"}) {
			t.Fatalf("ops = %q", got)
		}
		if c := e.cons(); c.dataPaths != 0o777 || c.addr != 01000 {
			t.Fatalf("console = %+v", c)
		}
	})
	t.Run("an action ignored when it runs gets an asynchronous hint", func(t *testing.T) {
		c := defaultControls()
		c.key = keyOff
		e := newEnv(t, withControls(c))
		e.m.Lock()
		e.m.PowerOnDelay = 100 * time.Millisecond
		e.m.Unlock()
		e.input(api.PanelInput{Kind: "key", Value: keyPower})
		if h := e.input(api.PanelInput{Client: 7, Kind: "momentary", Name: swExam, Active: true}); h != "" {
			t.Fatalf("hint %q", h)
		}
		e.idle()
		if st := e.m.State(); st != api.RunRunning {
			t.Fatalf("state %s", st)
		}
		if h := e.hintList(); len(h) != 1 || !strings.HasPrefix(h[0], "7:Works only when the CPU is halted") {
			t.Fatalf("hints = %q", h)
		}
		if n := e.m.Count("Examine"); n != 0 {
			t.Fatalf("Examine called %d times", n)
		}
	})
}
