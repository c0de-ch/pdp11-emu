// Package api defines the contracts shared by the pdp11d packages:
// internal/simh and internal/systems implement Machine, Stream, PanelSource
// and Systems; internal/panel and internal/web consume them.
//
// Changing anything here is a contract change: update docs/DESIGN.md too.
package api

import (
	"context"
	"errors"
)

// RunState is the state of the simulated machine as shown to users.
type RunState string

const (
	RunOff      RunState = "off"      // simulator process not running (power off)
	RunStarting RunState = "starting" // installing, starting or restarting the simulator
	RunHalted   RunState = "halted"   // CPU halted, SCP prompt available
	RunRunning  RunState = "running"  // CPU executing (including WAIT)
	RunStopping RunState = "stopping" // power-off in progress
	RunError    RunState = "error"    // process failed to start or died; LastError() says why
)

// Powered reports whether the CPU is available (halted or running).
func (s RunState) Powered() bool { return s == RunHalted || s == RunRunning }

// Speed selects how fast the simulated CPU runs.
type Speed string

const (
	SpeedFast      Speed = "fast"      // unthrottled, idles in WAIT
	SpeedAuthentic Speed = "authentic" // throttled to ~1 MIPS
)

// CPUMode is a PDP-11 processor mode as encoded in PSW<15:14>.
type CPUMode int

const (
	ModeKernel CPUMode = 0
	ModeSuper  CPUMode = 1
	ModeUser   CPUMode = 3
)

// Location identifies the target of a console EXAMINE or DEPOSIT.
type Location struct {
	// Register, when non-empty, names a SimH CPU register:
	// R00..R05, R10..R15, KSP, SSP, USP or PC. Addr is then ignored.
	Register string
	// Addr is a 22-bit physical address, or a 16-bit virtual address
	// when Virtual is set.
	Addr    uint32
	Virtual bool
	Mode    CPUMode // with Virtual: address space mode
	DSpace  bool    // with Virtual: data space (SimH -T switch, NOT -D)
}

// StopInfo describes why the CPU stopped.
type StopInfo struct {
	// Reason is the SimH text before ", PC:" ("HALT instruction",
	// "Front panel HALT", "Step expired", "Breakpoint", "Unit not attached",
	// trap names...). Empty when the CPU stopped without a stop line
	// (e.g. an EXPECT rule without "continue").
	Reason    string
	PC        uint16
	Insn      string // SimH disassembly, may be empty
	Requested bool   // the stop was requested by pdp11d (HALT switch, power off)
}

// EventKind classifies a MachineEvent.
type EventKind int

const (
	// EvState: State() changed; State is set. Not emitted for the
	// running->halted->running blips of Step or of transparent maintenance
	// stops (State() keeps reporting the outer state during those).
	EvState EventKind = iota
	// EvStop: the CPU stopped (not emitted for transparent maintenance
	// stops). Delivered before the EvState(RunHalted) it causes; for Step
	// only EvStop{Reason: "Step expired"} is delivered.
	EvStop
	// EvError: a message for the user (toast); Text is set.
	EvError
	// EvReady: the system's readyPattern appeared on the console after a
	// boot (Ready() became true).
	EvReady
)

// MachineEvent is delivered to Machine subscribers.
type MachineEvent struct {
	Kind  EventKind
	State RunState
	Stop  *StopInfo
	Text  string
}

// Stream is the PDP-11 console line. Console() returns the same Stream for
// the lifetime of the Machine; its history survives power cycles.
type Stream interface {
	// Write queues input for the PDP-11. Input is paced (large pastes) and
	// the queue is bounded (ErrInputFull). Returns ErrNotPowered while the
	// simulator's console is not connected.
	Write(p []byte) (int, error)
	// SubscribeHistory atomically snapshots the recent output (up to
	// 256 KiB) and registers fn for all output produced after the snapshot.
	// fn is called from the stream's reader goroutine with a buffer that is
	// only valid during the call; it must not block.
	SubscribeHistory(fn func(p []byte)) (history []byte, cancel func())
	// Connected reports whether the TCP connection to SimH is up.
	Connected() bool
	// Queued returns the number of input bytes waiting to be sent.
	Queued() int
	// CancelInput drops queued input.
	CancelInput()
}

// PowerOnOptions control Machine.PowerOn.
type PowerOnOptions struct {
	Boot bool // run the boot dialogue and the system's boot command once ready
}

// MachineStats are informational counters for the UI.
type MachineStats struct {
	MIPS          float64 `json:"mips"`          // executed instructions per second / 1e6
	SamplesPerSec uint32  `json:"samplesPerSec"` // panel export sample rate
	UptimeSec     float64 `json:"uptimeSec"`     // since the simulator started
}

// Errors returned by Machine and Stream methods (compare with errors.Is).
var (
	ErrNotPowered = errors.New("machine is powered off")
	ErrNotHalted  = errors.New("CPU is not halted")
	ErrBusy       = errors.New("machine is busy")
	ErrNoSystem   = errors.New("selected system is not installed")
	// ErrAddress wraps SimH's "Address space exceeded", "Relocation error"
	// and "Invalid argument" replies to EXAMINE/DEPOSIT (non-existent
	// memory, I/O-page hole, MMU abort, bad address).
	ErrAddress   = errors.New("address error")
	ErrInputFull = errors.New("console input queue full")
)

// Machine is the simulated PDP-11/70 (implemented by internal/simh).
//
// All methods are safe for concurrent use; operations that need the SCP
// channel are serialized internally. Events are delivered in order on one
// dedicated goroutine, never while internal locks are held; handlers must
// not block and must not call Machine methods synchronously (hand work to
// another goroutine).
type Machine interface {
	State() RunState
	LastError() string
	Subscribe(fn func(MachineEvent)) (cancel func())

	// PowerOn starts the simulator for the selected system. If the system
	// is not installed and its licence class is "redistributable" (or it is
	// listed in PDP11_ACCEPT_LICENSES), it is installed first (state
	// starting, progress via Systems.SubscribeProgress); otherwise
	// ErrNoSystem. Returns once the state is halted, running or error.
	PowerOn(ctx context.Context, opts PowerOnOptions) error
	// PowerOff halts the CPU and quits the simulator ("pulling the plug";
	// the guest OS is not shut down). It cancels a PowerOn in progress.
	PowerOff(ctx context.Context) error
	// Boot boots the selected system: the boot dialogue only works in a
	// fresh simulator, so if the CPU has executed anything since the
	// simulator started, Boot restarts the simulator first (state starting).
	Boot(ctx context.Context) error
	// Halt stops the CPU after the current instruction (no-op when halted).
	// Like a console halt it ends the WAIT state (Step too).
	Halt(ctx context.Context) error
	// Continue resumes execution at PC (SimH "cont").
	Continue(ctx context.Context) error
	// Step executes one instruction and returns when the CPU halted again.
	Step(ctx context.Context) error
	// Start performs a console START: reset all devices and run at addr
	// (SimH "run addr").
	Start(ctx context.Context, addr uint16) error
	// ConsoleReset performs a console START with HALT: reset, stay halted.
	ConsoleReset(ctx context.Context) error
	// Examine and Deposit need a halted CPU (ErrNotHalted otherwise); address
	// problems are returned wrapping ErrAddress.
	Examine(ctx context.Context, loc Location) (uint16, error)
	Deposit(ctx context.Context, loc Location, value uint16) error
	// Translate returns the 22-bit physical address of a virtual location
	// (MMU aware; identity for physical locations). Halted only.
	Translate(ctx context.Context, loc Location) (uint32, error)
	// SetSwitchRegister makes v the value programs read at 17777570. The
	// value is remembered while powered off and applied at every power-on
	// before the boot command.
	SetSwitchRegister(v uint16)

	// Speed is the speed of the running simulator. SetSpeed changes it
	// (transparently halting if needed); it never persists anything and
	// returns ErrNotPowered when off.
	Speed() Speed
	SetSpeed(ctx context.Context, s Speed) error

	// Console is the DL11 console line.
	Console() Stream
	// Ready reports whether the system's readyPattern was seen since the
	// last boot.
	Ready() bool
	// SystemID is the id of the system the simulator runs, "" when off.
	SystemID() string
	Stats() MachineStats
}

// Panel export sample flags (mirror simh/panel/pdp11_panel_shm.h).
const (
	PnlPAMask  uint32 = 0x003FFFFF
	PnlWR      uint32 = 1 << 22
	PnlByte    uint32 = 1 << 23
	PnlIFetch  uint32 = 1 << 24
	PnlVMode          = 25
	PnlMMode   uint32 = 3
	PnlWait    uint32 = 1 << 27
	PnlMMU     uint32 = 1 << 28
	PnlM22     uint32 = 1 << 29
	PnlDSpace  uint32 = 1 << 30
	PnlIOPage  uint32 = 1 << 31
	PnlHalted  uint32 = 0 // PanelSnapshot.State values
	PnlRunning uint32 = 1
	PnlWaiting uint32 = 2
)

// Sample is one bus-cycle sample exported by the simulator.
type Sample struct {
	Addr   uint32 // PA<21:0> | Pnl* flags
	Data   uint16 // bus data (WAIT sample: R0)
	VA     uint16 // virtual address (WAIT sample: PC)
	Weight uint32 // wall-clock microseconds this sample stands for (>= 1)
	PC     uint16
	IR     uint16
}

// PanelSnapshot is the register block and status of the panel export.
// State distinguishes running from WAIT only; whether the CPU is halted
// is decided by Machine.State().
type PanelSnapshot struct {
	State          uint32 // PnlHalted / PnlRunning / PnlWaiting
	R              [8]uint16
	PSW            uint16
	DR             uint16
	MMR0           uint16
	MMR3           uint16
	LastPA         uint32 // PA + flags of the most recent bus cycle
	LastData       uint16
	PCPA           uint32 // physical address of PC
	SimTime        float64
	InstrCount     uint64 // instructions executed (approximate, sum of sample intervals)
	SampleInterval uint32
	SamplesPerSec  uint32
	TrapCount      uint32
	StopCount      uint32
	RingWr         uint32
	// DROn[i] is the weight of the samples taken while DISPLAY REGISTER
	// bit i was 1, WeightSum the weight of all samples (wall µs, modulo
	// 2^32). Their differences between two snapshots give the
	// time-weighted DISPLAY REGISTER in between.
	DROn      [16]uint32
	WeightSum uint32
}

// PanelSource reads the simulator's front-panel export (internal/shm).
type PanelSource interface {
	// ReadFrame copies the samples written since the previous call into dst
	// (at most len(dst); the newest ones when more are pending) and returns
	// the latest snapshot. ok is false when no export is attached.
	ReadFrame(dst []Sample) (n int, snap PanelSnapshot, ok bool)
}

// Progress reports a long-running task.
type Progress struct {
	Task  string  `json:"task"`  // install | factoryReset | shutdown
	ID    string  `json:"id"`    // system id
	Phase string  `json:"phase"` // download | verify | extract | step | done | error | cancelled
	Pct   float64 `json:"pct"`
	Bytes int64   `json:"bytes"`
	Total int64   `json:"total"`
	Text  string  `json:"text"`
	Req   string  `json:"req,omitempty"` // client correlation id, if any
}

// LicenseInfo is the licence shown before installing a system.
type LicenseInfo struct {
	Name    string `json:"name"`
	Class   string `json:"class"` // redistributable | hobbyist
	Summary string `json:"summary"`
	Text    string `json:"text"`
}

// Credential is a login account documented for a system.
type Credential struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Note     string `json:"note,omitempty"`
}

// ConsoleKeys describes how the browser terminal should map keys.
type ConsoleKeys struct {
	Backspace string `json:"backspace"`         // del | bs | "#" (character sent for Backspace)
	Interrupt string `json:"interrupt"`         // ctrl-c | del (what Ctrl+C sends)
	KeyHelp   string `json:"keyHelp,omitempty"` // short help shown in the console header
}

// SystemInfo is one entry of the systems list (see docs/PROTOCOL.md).
type SystemInfo struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Short         string       `json:"short"`
	Category      string       `json:"category"`
	Description   string       `json:"description"`
	Order         int          `json:"order"`
	Default       bool         `json:"default"`
	Installed     bool         `json:"installed"`
	Installing    bool         `json:"installing"`
	DownloadBytes int64        `json:"downloadBytes"`
	DiskBytes     int64        `json:"diskBytes"`
	License       LicenseInfo  `json:"license"`
	Credentials   []Credential `json:"credentials"`
	// NoLogin: the console needs no login (RT-11, RSX-11M-PLUS V3.0); the
	// UI never tells the user to log in on such a system.
	NoLogin bool `json:"noLogin"`
	// BootTimeoutSec: a boot that has not reached readyPattern after this
	// many seconds gets one warning toast (catalog bootTimeoutSec, default
	// 120).
	BootTimeoutSec int         `json:"bootTimeoutSec"`
	Shutdown       string      `json:"shutdown"`    // human-readable clean shutdown instructions
	CanShutdown    bool        `json:"canShutdown"` // a shutdown script exists (shutdownOS)
	Notes          string      `json:"notes"`       // markdown: "Log in", "Things to try", "Shut down"
	Speed          Speed       `json:"speed"`       // effective speed (user override or default)
	Console        ConsoleKeys `json:"console"`
}

// Systems manages the catalog, installation and selection (internal/systems).
type Systems interface {
	List() []SystemInfo
	Get(id string) (SystemInfo, bool)
	// Selected returns the selected system id; Select changes it (callers
	// ensure the machine is off or in error; while a simulator runs it
	// fails with ErrBusy).
	Selected() string
	Select(id string) error
	// Install downloads (cache, import dir, url, mirrors), verifies and
	// extracts a system. acceptLicense must be true for classes other than
	// "redistributable" unless the id is in PDP11_ACCEPT_LICENSES. Installing
	// an installed system reports "done" immediately; installs are
	// serialized; a second install of the same id, or one while a simulator
	// uses the system's images, returns ErrBusy.
	Install(ctx context.Context, id string, acceptLicense bool, req string) error
	// FactoryReset restores the working disk images from the pristine
	// archive (ErrBusy like Install). A cancel or a failure leaves the old
	// images installed.
	FactoryReset(ctx context.Context, id string, req string) error
	// Cancel cancels a running install or factory reset of id.
	Cancel(id string)
	// SubscribeProgress receives progress of every install/factory reset,
	// whoever started it. Callbacks must not block.
	SubscribeProgress(fn func(Progress)) (cancel func())
	// SubscribeChanges is called (coalesced) whenever List() would change.
	SubscribeChanges(fn func()) (cancel func())
	// SpeedFor returns the effective speed of a system (user override or
	// default); SetSpeedFor persists a user override.
	SpeedFor(id string) Speed
	SetSpeedFor(id string, s Speed) error
	// ShutdownScript returns the clean-shutdown steps of a system (nil if none).
	ShutdownScript(id string) []ScriptStep
}

// ScriptStep waits for Expect (if set) on the console, then types Send.
type ScriptStep struct {
	Expect     string `json:"expect,omitempty"`
	Send       string `json:"send,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"` // default 30
}

// LogBuffer holds recent simulator output lines (the only source of the
// "log" channel).
type LogBuffer interface {
	// Lines returns up to n of the most recent lines, oldest first
	// (n <= 0: all).
	Lines(n int) []string
	// Subscribe registers fn for every line added from now on.
	Subscribe(fn func(line string)) (cancel func())
	// SubscribeHistory atomically returns up to n of the most recent lines
	// (as Lines) and registers fn for all lines added after them: every
	// line is either in history or passed to fn, never both. fn receives
	// lines in order, must not block and must not call back into the
	// buffer; the slice is only valid during the call.
	SubscribeHistory(n int, fn func(lines []string)) (history []string, cancel func())
}

// Front panel controls as exchanged with browsers (docs/PROTOCOL.md "controls").
type Controls struct {
	SR         uint32          `json:"sr"`
	EnableHalt string          `json:"enableHalt"` // enable | halt
	SinstSbus  string          `json:"sinstSbus"`  // inst | bus
	AddrKnob   int             `json:"addrKnob"`   // 0..7
	DataKnob   int             `json:"dataKnob"`   // 0..7
	Key        string          `json:"key"`        // off | power | lock
	LampTest   bool            `json:"lampTest"`
	Pressed    map[string]bool `json:"pressed"` // LOAD_ADRS EXAM DEP CONT START
}

// ConsoleInfo is the console (panel) state shown in the UI.
type ConsoleInfo struct {
	Addr     uint32 `json:"addr"`     // console address register (22 bits)
	AdrsErr  bool   `json:"adrsErr"`  // static console ADRS ERR
	LastStop string `json:"lastStop"` // reason of the last stop
	PC       uint16 `json:"pc"`       // PC at the last stop
	Busy     string `json:"busy"`     // "" or the panel operation in progress ("shutdown", "boot", ...)
}

// PanelInput is a decoded front-panel or machine-control message.
type PanelInput struct {
	Client uint64 // connection id (momentary switches are released when it goes away)
	Req    string // client correlation id echoed in resulting toasts/progress
	// Kind: "sr" | "momentary" | "latch" | "knob" | "key" | "boot" |
	// "shutdownOS" | "retryPower"
	Kind   string
	Name   string // momentary/latch/knob name
	Value  string // latch value, key position
	Int    int    // sr value, knob position
	Active bool   // momentary pressed
}

// Lamp indices of the 64-byte panel frame (docs/PROTOCOL.md).
const (
	LampA0         = 0  // .. LampA0+21 = A21
	LampD0         = 22 // .. LampD0+15 = D15
	LampParityLow  = 38
	LampParityHigh = 39
	LampParErr     = 40
	LampAdrsErr    = 41
	LampRun        = 42
	LampPause      = 43
	LampMaster     = 44
	LampUser       = 45
	LampSuper      = 46
	LampKernel     = 47
	LampData       = 48 // the "DATA" (D space) status lamp; r4 calls it DATA_SPACE
	LampAdrs16     = 49
	LampAdrs18     = 50
	LampAdrs22     = 51
	LampUserD      = 52
	LampSuperD     = 53
	LampKernelD    = 54
	LampConsPhy    = 55
	LampUserI      = 56
	LampSuperI     = 57
	LampKernelI    = 58
	LampProgPhy    = 59
	LampDataPaths  = 60
	LampBusReg     = 61
	LampUAdrs      = 62
	LampDisplayReg = 63
	LampCount      = 64
)

// ADDRESS knob positions (clockwise) and DATA knob functions.
const (
	AddrProgPhy = 0
	AddrConsPhy = 1
	AddrKernelD = 2
	AddrSuperD  = 3
	AddrUserD   = 4
	AddrUserI   = 5
	AddrSuperI  = 6
	AddrKernelI = 7

	DataBusReg     = 0 // dataKnob % 4
	DataDataPaths  = 1
	DataUAdrs      = 2
	DataDisplayReg = 3
)
