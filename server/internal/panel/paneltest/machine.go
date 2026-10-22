// Package paneltest provides in-memory fakes of the api interfaces
// (Machine, Stream, PanelSource, Systems, LogBuffer) for tests of the panel
// and web packages. The fakes follow the api contracts closely: Machine
// events are delivered in order on one goroutine and never under locks,
// Stream callbacks get a buffer that is only valid during the call, and
// Examine/Deposit report address problems wrapping api.ErrAddress.
package paneltest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"pdp11emu/server/internal/api"
)

// Call records one Machine method call.
type Call struct {
	Name  string // method name
	Loc   api.Location
	Value uint16 // Deposit value, Start address, SetSwitchRegister value
	Boot  bool   // PowerOn option
	Speed api.Speed
}

// Machine is a fake api.Machine with 256 KB of memory, an I/O page with
// the switch/display register and the PSW, and the general registers.
type Machine struct {
	// Behaviour; set before use (or between operations under Lock/Unlock).
	NoSystem     bool          // PowerOn returns api.ErrNoSystem
	PowerOnErr   error         // PowerOn fails with this error (state error)
	PowerOnDelay time.Duration // time spent in state starting
	BootSystemID string        // SystemID while powered (default "bsd211")
	MemSize      uint32        // bytes of main memory (default 256 KB)
	MMU          func(loc api.Location) (uint32, error)
	HaltErr      error
	// BeforeOp, if set, is called (without locks) at the start of every
	// CPU operation with the method name, e.g. to change the state under
	// the panel's feet.
	BeforeOp func(name string)

	mu        sync.Mutex
	state     api.RunState
	lastError string
	speed     api.Speed
	systemID  string
	ready     bool
	sr        uint16
	srCount   int
	executed  bool
	pc        uint16
	mem       map[uint32]uint16
	iopage    map[uint32]uint16
	regs      map[string]uint16
	calls     []Call
	stats     api.MachineStats
	powerGen  int
	powerStop chan struct{}

	console *Stream

	evMu    sync.Mutex
	evCond  *sync.Cond
	evq     []api.MachineEvent
	evBusy  bool
	evSubs  map[int]func(api.MachineEvent)
	evNext  int
	evClose bool
	evDone  chan struct{}
}

// NewMachine returns a powered-off fake machine. Call Close when done.
func NewMachine() *Machine {
	m := &Machine{
		state:     api.RunOff,
		speed:     api.SpeedFast,
		mem:       map[uint32]uint16{},
		iopage:    map[uint32]uint16{017777570: 0, 017777776: 0, 017777572: 0},
		regs:      map[string]uint16{},
		console:   NewStream(),
		evSubs:    map[int]func(api.MachineEvent){},
		evDone:    make(chan struct{}),
		powerStop: make(chan struct{}),
	}
	for _, r := range []string{"R00", "R01", "R02", "R03", "R04", "R05", "R10", "R11", "R12", "R13", "R14", "R15", "KSP", "SSP", "USP", "PC"} {
		m.regs[r] = 0
	}
	m.evCond = sync.NewCond(&m.evMu)
	go m.dispatch()
	return m
}

// Close stops event delivery.
func (m *Machine) Close() {
	m.evMu.Lock()
	m.evClose = true
	m.evCond.Broadcast()
	m.evMu.Unlock()
	<-m.evDone
}

// Lock and Unlock guard changes of the behaviour fields after use started.
func (m *Machine) Lock()   { m.mu.Lock() }
func (m *Machine) Unlock() { m.mu.Unlock() }

func (m *Machine) dispatch() {
	defer close(m.evDone)
	m.evMu.Lock()
	for {
		for len(m.evq) == 0 && !m.evClose {
			m.evCond.Wait()
		}
		if m.evClose {
			m.evMu.Unlock()
			return
		}
		ev := m.evq[0]
		m.evq = m.evq[1:]
		m.evBusy = true
		subs := make([]func(api.MachineEvent), 0, len(m.evSubs))
		keys := make([]int, 0, len(m.evSubs))
		for k := range m.evSubs {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			subs = append(subs, m.evSubs[k])
		}
		m.evMu.Unlock()
		for _, fn := range subs {
			fn(ev)
		}
		m.evMu.Lock()
		m.evBusy = false
		m.evCond.Broadcast()
	}
}

// Sync waits until every event emitted so far has been delivered.
func (m *Machine) Sync() {
	m.evMu.Lock()
	for (len(m.evq) > 0 || m.evBusy) && !m.evClose {
		m.evCond.Wait()
	}
	m.evMu.Unlock()
}

func (m *Machine) emit(ev api.MachineEvent) {
	m.evMu.Lock()
	m.evq = append(m.evq, ev)
	m.evCond.Broadcast()
	m.evMu.Unlock()
}

// setStateLocked changes the state and emits EvState. Caller holds m.mu.
func (m *Machine) setStateLocked(st api.RunState) {
	if m.state == st {
		return
	}
	m.state = st
	m.emit(api.MachineEvent{Kind: api.EvState, State: st})
}

func (m *Machine) record(c Call) { m.calls = append(m.calls, c) }

func (m *Machine) before(name string) {
	m.mu.Lock()
	fn := m.BeforeOp
	m.mu.Unlock()
	if fn != nil {
		fn(name)
	}
}

// Subscribe implements api.Machine.
func (m *Machine) Subscribe(fn func(api.MachineEvent)) (cancel func()) {
	m.evMu.Lock()
	defer m.evMu.Unlock()
	m.evNext++
	id := m.evNext
	m.evSubs[id] = fn
	return func() {
		m.evMu.Lock()
		delete(m.evSubs, id)
		m.evMu.Unlock()
	}
}

func (m *Machine) State() api.RunState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Machine) LastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastError
}

// PowerOn implements api.Machine: starting → halted (→ running with Boot).
func (m *Machine) PowerOn(ctx context.Context, o api.PowerOnOptions) error {
	m.mu.Lock()
	m.record(Call{Name: "PowerOn", Boot: o.Boot})
	switch m.state {
	case api.RunOff, api.RunError:
	default:
		m.mu.Unlock()
		return api.ErrBusy
	}
	if m.NoSystem {
		m.lastError = "rt11 is not installed: accept its licence and install it"
		m.mu.Unlock()
		return fmt.Errorf("%w: rt11", api.ErrNoSystem)
	}
	m.lastError = ""
	m.setStateLocked(api.RunStarting)
	if err := m.PowerOnErr; err != nil {
		m.lastError = err.Error()
		m.setStateLocked(api.RunError)
		m.mu.Unlock()
		return err
	}
	gen, stop, delay := m.powerGen, m.powerStop, m.PowerOnDelay
	m.mu.Unlock()
	if delay > 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-stop:
			return errors.New("power on cancelled by power off")
		case <-ctx.Done():
			m.mu.Lock()
			if m.powerGen == gen {
				m.setStateLocked(api.RunOff)
			}
			m.mu.Unlock()
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.powerGen != gen {
		return errors.New("power on cancelled by power off")
	}
	m.executed = false
	m.pc = 0104 // a fresh SimH sits in its clock calibration loop
	m.systemID = m.BootSystemID
	if m.systemID == "" {
		m.systemID = "bsd211"
	}
	m.console.SetConnected(true)
	m.setStateLocked(api.RunHalted)
	if o.Boot {
		m.executed = true
		m.setStateLocked(api.RunRunning)
	}
	return nil
}

// PowerOff implements api.Machine; it cancels a PowerOn in progress.
func (m *Machine) PowerOff(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "PowerOff"})
	if m.state == api.RunOff {
		return nil
	}
	m.powerGen++
	close(m.powerStop)
	m.powerStop = make(chan struct{})
	if m.state == api.RunRunning {
		m.emit(api.MachineEvent{Kind: api.EvStop, Stop: &api.StopInfo{Reason: "Front panel HALT", PC: m.pc, Requested: true}})
	}
	m.setStateLocked(api.RunStopping)
	m.systemID = ""
	m.ready = false
	m.console.SetConnected(false)
	m.setStateLocked(api.RunOff)
	return nil
}

// Boot implements api.Machine (restarts the simulator if it executed).
func (m *Machine) Boot(ctx context.Context) error {
	m.before("Boot")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "Boot"})
	if !m.state.Powered() {
		return api.ErrNotPowered
	}
	if m.executed {
		m.setStateLocked(api.RunStarting)
		m.setStateLocked(api.RunHalted)
	}
	m.executed = true
	m.ready = false
	m.setStateLocked(api.RunRunning)
	return nil
}

// Halt implements api.Machine.
func (m *Machine) Halt(ctx context.Context) error {
	m.before("Halt")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "Halt"})
	if m.HaltErr != nil {
		return m.HaltErr
	}
	if m.state != api.RunRunning {
		return nil
	}
	m.stopLocked(api.StopInfo{Reason: "Front panel HALT", PC: m.pc, Requested: true})
	return nil
}

func (m *Machine) stopLocked(s api.StopInfo) {
	m.pc = s.PC
	m.regs["PC"] = s.PC
	m.emit(api.MachineEvent{Kind: api.EvStop, Stop: &s})
	m.setStateLocked(api.RunHalted)
}

func (m *Machine) needHalted() error {
	switch m.state {
	case api.RunHalted:
		return nil
	case api.RunRunning:
		return api.ErrNotHalted
	}
	return api.ErrNotPowered
}

// Continue implements api.Machine.
func (m *Machine) Continue(ctx context.Context) error {
	m.before("Continue")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "Continue"})
	if err := m.needHalted(); err != nil {
		return err
	}
	m.executed = true
	m.setStateLocked(api.RunRunning)
	return nil
}

// Step implements api.Machine: PC += 2, EvStop "Step expired" only.
func (m *Machine) Step(ctx context.Context) error {
	m.before("Step")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "Step"})
	if err := m.needHalted(); err != nil {
		return err
	}
	m.executed = true
	m.pc += 2
	m.regs["PC"] = m.pc
	m.emit(api.MachineEvent{Kind: api.EvStop, Stop: &api.StopInfo{Reason: "Step expired", PC: m.pc}})
	return nil
}

// Start implements api.Machine.
func (m *Machine) Start(ctx context.Context, addr uint16) error {
	m.before("Start")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "Start", Value: addr})
	if err := m.needHalted(); err != nil {
		return err
	}
	m.executed = true
	m.pc = addr
	m.setStateLocked(api.RunRunning)
	return nil
}

// ConsoleReset implements api.Machine.
func (m *Machine) ConsoleReset(ctx context.Context) error {
	m.before("ConsoleReset")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "ConsoleReset"})
	return m.needHalted()
}

func addrErr(what string) error { return fmt.Errorf("%w: %s", api.ErrAddress, what) }

// physical resolves a location to a physical address. Caller holds m.mu.
func (m *Machine) physical(loc api.Location) (uint32, error) {
	if !loc.Virtual {
		return loc.Addr & 017777777, nil
	}
	if m.MMU != nil {
		return m.MMU(loc)
	}
	va := loc.Addr & 0177777
	if va >= 0160000 {
		return 017600000 | va, nil
	}
	return va, nil
}

func (m *Machine) memSize() uint32 {
	if m.MemSize != 0 {
		return m.MemSize
	}
	return 256 << 10
}

// Examine implements api.Machine.
func (m *Machine) Examine(ctx context.Context, loc api.Location) (uint16, error) {
	m.before("Examine")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "Examine", Loc: loc})
	if err := m.needHalted(); err != nil {
		return 0, err
	}
	if loc.Register != "" {
		v, ok := m.regs[loc.Register]
		if !ok {
			return 0, fmt.Errorf("unknown register %s", loc.Register)
		}
		return v, nil
	}
	pa, err := m.physical(loc)
	if err != nil {
		return 0, err
	}
	pa &^= 1
	switch {
	case pa >= 017760000:
		v, ok := m.iopage[pa]
		if !ok {
			return 0, addrErr("Address space exceeded")
		}
		if pa == 017777570 {
			return m.sr, nil
		}
		return v, nil
	case pa < m.memSize():
		return m.mem[pa], nil
	}
	return 0, addrErr("Address space exceeded")
}

// Deposit implements api.Machine.
func (m *Machine) Deposit(ctx context.Context, loc api.Location, v uint16) error {
	m.before("Deposit")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "Deposit", Loc: loc, Value: v})
	if err := m.needHalted(); err != nil {
		return err
	}
	if loc.Register != "" {
		if _, ok := m.regs[loc.Register]; !ok {
			return fmt.Errorf("unknown register %s", loc.Register)
		}
		m.regs[loc.Register] = v
		return nil
	}
	pa, err := m.physical(loc)
	if err != nil {
		return err
	}
	pa &^= 1
	switch {
	case pa >= 017760000:
		if _, ok := m.iopage[pa]; !ok {
			return addrErr("Address space exceeded")
		}
		m.iopage[pa] = v
	case pa < m.memSize():
		m.mem[pa] = v
	default:
		return addrErr("Address space exceeded")
	}
	return nil
}

// Translate implements api.Machine.
func (m *Machine) Translate(ctx context.Context, loc api.Location) (uint32, error) {
	m.before("Translate")
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "Translate", Loc: loc})
	if err := m.needHalted(); err != nil {
		return 0, err
	}
	return m.physical(loc)
}

// SetSwitchRegister implements api.Machine.
func (m *Machine) SetSwitchRegister(v uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sr = v
	m.srCount++
}

func (m *Machine) Speed() api.Speed {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.speed
}

func (m *Machine) SetSpeed(ctx context.Context, s api.Speed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(Call{Name: "SetSpeed", Speed: s})
	if !m.state.Powered() {
		return api.ErrNotPowered
	}
	m.speed = s
	return nil
}

func (m *Machine) Console() api.Stream { return m.console }

func (m *Machine) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

func (m *Machine) SystemID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.systemID
}

func (m *Machine) Stats() api.MachineStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

// --- test helpers ---

// Stream returns the console as the fake type.
func (m *Machine) Stream() *Stream { return m.console }

// SetState changes the state as if the simulator did it on its own.
func (m *Machine) SetState(st api.RunState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setStateLocked(st)
}

// StopCPU stops a running CPU as the guest would (HALT instruction,
// trap...): EvStop, then EvState(halted).
func (m *Machine) StopCPU(reason string, pc uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != api.RunRunning {
		return
	}
	m.stopLocked(api.StopInfo{Reason: reason, PC: pc})
}

// SetReady sets Ready and emits EvReady.
func (m *Machine) SetReady(r bool) {
	m.mu.Lock()
	m.ready = r
	m.mu.Unlock()
	if r {
		m.emit(api.MachineEvent{Kind: api.EvReady})
	}
}

// EmitError emits an EvError.
func (m *Machine) EmitError(text string) {
	m.emit(api.MachineEvent{Kind: api.EvError, Text: text})
}

// SetStats sets what Stats returns.
func (m *Machine) SetStats(s api.MachineStats) {
	m.mu.Lock()
	m.stats = s
	m.mu.Unlock()
}

// SetLastError sets what LastError returns.
func (m *Machine) SetLastError(s string) {
	m.mu.Lock()
	m.lastError = s
	m.mu.Unlock()
}

// SR returns the last value given to SetSwitchRegister and how many calls there were.
func (m *Machine) SR() (uint16, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sr, m.srCount
}

// Mem returns the memory word at physical address pa.
func (m *Machine) Mem(pa uint32) uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pa >= 017760000 {
		return m.iopage[pa&^1]
	}
	return m.mem[pa&^1]
}

// SetMem stores a memory word at physical address pa.
func (m *Machine) SetMem(pa uint32, v uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mem[pa&^1] = v
}

// Reg returns a register.
func (m *Machine) Reg(name string) uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.regs[name]
}

// SetReg sets a register.
func (m *Machine) SetReg(name string, v uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.regs[name] = v
}

// PC returns the program counter.
func (m *Machine) PC() uint16 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pc
}

// SetIOPage makes an I/O page register exist (and sets it).
func (m *Machine) SetIOPage(pa uint32, v uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.iopage[pa&^1] = v
}

// SetPC sets the program counter.
func (m *Machine) SetPC(pc uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pc = pc
	m.regs["PC"] = pc
}

// Calls returns the method calls so far.
func (m *Machine) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// CallNames returns the names of the method calls so far.
func (m *Machine) CallNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	for i, c := range m.calls {
		out[i] = c.Name
	}
	return out
}

// ResetCalls forgets the recorded calls.
func (m *Machine) ResetCalls() {
	m.mu.Lock()
	m.calls = nil
	m.mu.Unlock()
}

// Count returns how often method name was called.
func (m *Machine) Count(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c.Name == name {
			n++
		}
	}
	return n
}

// WaitFor polls cond until it is true or the timeout expires.
func WaitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

var _ api.Machine = (*Machine)(nil)
