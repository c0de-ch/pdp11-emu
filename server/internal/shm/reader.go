package shm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"pdp11emu/server/internal/api"
)

// Reader maps the panel export file. It implements api.PanelSource.
//
// Every word shared with the simulator is accessed with sync/atomic (the
// protocol in pdp11_panel_shm.h relies on acquire/release ordering, which
// matters on arm64). All methods are safe for concurrent use; ReadFrame
// consumes the sample ring, so only one consumer should call it.
type Reader struct {
	path string

	mu    sync.RWMutex // write-locked only by Close
	mem   []byte       // the mapping; nil after Close
	unmap func() error

	frameMu sync.Mutex // serializes ring consumption
	next    uint32     // ring_wr value of the next sample to consume
	last    api.PanelSnapshot

	dropped atomic.Uint64
}

// snapshotRetries bounds the seqlock loop. The simulator's write section is
// a few dozen stores, so a consistent copy normally succeeds at once; the
// bound only matters if the simulator died inside the write section.
const snapshotRetries = 1000

// ErrFormat reports a file that is not a version-3 panel export.
var ErrFormat = errors.New("not a PDP-11 panel export (version 3)")

var littleEndian = binary.NativeEndian.Uint16([]byte{1, 0}) == 1

// Open maps an existing export file (written by "ATTACH PANEL <path>") and
// checks magic, version and size. Like ATTACH it refuses a symbolic link, a
// file of another user and a file open to group or others: another local
// user who created it first (in a world-writable directory such as
// /dev/shm) could map it too, read the bus samples and write halt requests.
func Open(path string) (*Reader, error) {
	if !littleEndian {
		return nil, fmt.Errorf("shm: %s: the panel export is little-endian; big-endian hosts are not supported", path)
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("shm: %s is a symbolic link; the panel export must be a regular file", path)
	}
	if err != nil {
		return nil, fmt.Errorf("shm: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("shm: %w", err)
	}
	if err := checkFile(path, st, os.Geteuid()); err != nil {
		return nil, err
	}
	if st.Size() != FileSize {
		return nil, fmt.Errorf("shm: %s: %w: file size %d, want %d", path, ErrFormat, st.Size(), FileSize)
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, FileSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("shm: mmap %s: %w", path, err)
	}
	r, err := newReader(path, mem, func() error { return syscall.Munmap(mem) })
	if err != nil {
		_ = syscall.Munmap(mem)
		return nil, err
	}
	return r, nil
}

// checkFile refuses an export file that is not a regular file of the user
// euid or that grants group or others any access.
func checkFile(path string, st os.FileInfo, euid int) error {
	if !st.Mode().IsRegular() {
		return fmt.Errorf("shm: %s is not a regular file", path)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != euid {
		return fmt.Errorf("shm: %s belongs to uid %d, not to this user (uid %d)", path, sys.Uid, euid)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("shm: %s is open to other users (mode %04o; it must be 0600)", path, perm)
	}
	return nil
}

// newReader wraps a mapping (or, in tests, an 8-byte aligned buffer) of
// FileSize bytes after validating its header.
func newReader(path string, mem []byte, unmap func() error) (*Reader, error) {
	if len(mem) != FileSize {
		return nil, fmt.Errorf("shm: %s: %w: mapping size %d", path, ErrFormat, len(mem))
	}
	if uintptr(unsafe.Pointer(&mem[0]))%8 != 0 {
		return nil, fmt.Errorf("shm: %s: mapping is not 8-byte aligned", path)
	}
	r := &Reader{path: path, mem: mem, unmap: unmap}
	if got := r.load(OffMagic); got != Magic {
		return nil, fmt.Errorf("shm: %s: %w: magic %#08x", path, ErrFormat, got)
	}
	if got := r.load(OffVersion); got != Version {
		return nil, fmt.Errorf("shm: %s: %w: layout version %d (the simulator and pdp11d come from different builds)", path, ErrFormat, got)
	}
	if got := r.load(OffSize); got != FileSize {
		return nil, fmt.Errorf("shm: %s: %w: header size %d", path, ErrFormat, got)
	}
	if got := r.load(OffRingSize); got != RingSize {
		return nil, fmt.Errorf("shm: %s: %w: ring size %d", path, ErrFormat, got)
	}
	r.next = r.load(OffRingWr) // samples written before we attached are history
	return r, nil
}

// Path returns the file the reader maps.
func (r *Reader) Path() string { return r.path }

// Close unmaps the file. Further calls are no-ops (ReadFrame reports !ok).
func (r *Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mem == nil {
		return nil
	}
	r.mem = nil
	if r.unmap != nil {
		return r.unmap()
	}
	return nil
}

func (r *Reader) word(off int) *uint32 { return (*uint32)(unsafe.Pointer(&r.mem[off])) }
func (r *Reader) load(off int) uint32  { return atomic.LoadUint32(r.word(off)) }
func (r *Reader) store(off int, v uint32) {
	atomic.StoreUint32(r.word(off), v)
}
func (r *Reader) load64(off int) uint64 {
	return atomic.LoadUint64((*uint64)(unsafe.Pointer(&r.mem[off])))
}

// ReadFrame implements api.PanelSource. It copies the samples written since
// the previous call into dst (the newest len(dst) when more are pending) and
// returns the latest snapshot. A consumer that fell more than RingSize/2
// behind skips ahead, because older entries may be overwritten while they
// are read; skipped samples are counted by Dropped.
func (r *Reader) ReadFrame(dst []api.Sample) (int, api.PanelSnapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mem == nil {
		return 0, api.PanelSnapshot{}, false
	}
	r.frameMu.Lock()
	defer r.frameMu.Unlock()

	wr := r.load(OffRingWr) // acquire: entries before wr are complete
	pending := wr - r.next  // modulo 2^32
	if pending > RingSize/2 {
		r.dropped.Add(uint64(pending - RingSize/2))
		pending = RingSize / 2
	}
	n := int(pending)
	if n > len(dst) {
		r.dropped.Add(uint64(n - len(dst)))
		n = len(dst)
	}
	start := wr - uint32(n)
	for i := 0; i < n; i++ {
		dst[i] = r.sample(start + uint32(i))
	}
	// If the producer lapped us during the copy, the oldest entries may be
	// torn: entry k is only safe while the producer has not begun writing
	// entry k+RingSize, i.e. while k > ring_wr-RingSize.
	if behind := r.load(OffRingWr) - start; n > 0 && behind >= RingSize {
		bad := int(behind - RingSize + 1)
		if bad > n {
			bad = n
		}
		copy(dst, dst[bad:n])
		n -= bad
		r.dropped.Add(uint64(bad))
	}
	r.next = wr
	return n, r.snapshot(), true
}

// sample reads ring entry number k (k counts samples, modulo 2^32).
func (r *Reader) sample(k uint32) api.Sample {
	off := OffRing + int(k%RingSize)*SampleSize
	w0 := r.load(off)      // addr
	w1 := r.load(off + 4)  // data | va<<16
	w2 := r.load(off + 8)  // weight
	w3 := r.load(off + 12) // pc | ir<<16
	return api.Sample{
		Addr:   w0,
		Data:   uint16(w1),
		VA:     uint16(w1 >> 16),
		Weight: w2,
		PC:     uint16(w3),
		IR:     uint16(w3 >> 16),
	}
}

// Snapshot returns the register block and status without consuming samples.
func (r *Reader) Snapshot() (api.PanelSnapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mem == nil {
		return api.PanelSnapshot{}, false
	}
	r.frameMu.Lock()
	defer r.frameMu.Unlock()
	return r.snapshot(), true
}

// snapshot copies the seqlock-protected register block plus the
// independently updated status words. Caller holds frameMu and mu.
func (r *Reader) snapshot() api.PanelSnapshot {
	s := r.last
	for i := 0; i < snapshotRetries; i++ {
		seq := r.load(OffSeq)
		if seq&1 == 0 {
			var b api.PanelSnapshot
			for j := range b.R {
				b.R[j] = uint16(r.load(OffR + 4*j))
			}
			b.PSW = uint16(r.load(OffPSW))
			b.DR = uint16(r.load(OffDR))
			b.MMR0 = uint16(r.load(OffMMR0))
			b.MMR3 = uint16(r.load(OffMMR3))
			b.LastPA = r.load(OffLastPA)
			b.LastData = uint16(r.load(OffLastData))
			b.SimTime = math.Float64frombits(r.load64(OffSimTime))
			b.SampleInterval = r.load(OffSampleInterval)
			b.SamplesPerSec = r.load(OffSamplesPerSec)
			b.PCPA = r.load(OffPCPA)
			b.InstrCount = r.load64(OffInstrCount)
			if r.load(OffSeq) == seq {
				s = b
				break
			}
		}
		if i >= 8 {
			runtime.Gosched()
		}
	}
	// Not covered by the seqlock: each is a single word of its own.
	s.State = r.load(OffState)
	s.TrapCount = r.load(OffTrapCount)
	s.StopCount = r.load(OffStopCount)
	s.RingWr = r.load(OffRingWr)
	// Not one snapshot either: a sample stored while these are read can be
	// in some of them only; the next read has all of it.
	for i := range s.DROn {
		s.DROn[i] = r.load(OffDROn + 4*i)
	}
	s.WeightSum = r.load(OffWeightSum)
	r.last = s
	return s
}

// Dropped returns the number of samples the consumer skipped because it
// fell behind or dst was too small.
func (r *Reader) Dropped() uint64 { return r.dropped.Load() }

// SetSR writes the switch register value programs read and marks it valid.
func (r *Reader) SetSR(v uint16) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mem == nil {
		return
	}
	r.store(OffSR, uint32(v))
	r.store(OffSRValid, 1) // after sr: a reader that sees sr_valid sees sr
}

// RequestHalt stores halt_req = halt_ack + 1 (see the handshake in
// pdp11_panel_shm.h). Call only while the CPU is believed to run.
func (r *Reader) RequestHalt() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mem == nil {
		return
	}
	r.store(OffHaltReq, r.load(OffHaltAck)+1)
}

// CancelHalt stores halt_req = halt_ack. Call after every stop that was
// not "Front panel HALT" and before writing any run command.
func (r *Reader) CancelHalt() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mem == nil {
		return
	}
	r.store(OffHaltReq, r.load(OffHaltAck))
}

// HaltAck returns the halt_req value the simulator last honoured.
func (r *Reader) HaltAck() uint32 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mem == nil {
		return 0
	}
	return r.load(OffHaltAck)
}

// HaltPending reports whether a halt request has not been honoured yet.
func (r *Reader) HaltPending() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mem == nil {
		return false
	}
	return r.load(OffHaltReq) != r.load(OffHaltAck)
}

// SetTargetRate sets the desired samples per second (0 = simulator default).
func (r *Reader) SetTargetRate(n uint32) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.mem == nil {
		return
	}
	r.store(OffTargetRate, n)
}

var _ api.PanelSource = (*Reader)(nil)
