package paneltest

import (
	"sync"

	"pdp11emu/server/internal/api"
)

// Source is a fake api.PanelSource: tests push samples and set the
// snapshot; ReadFrame consumes the pending samples like the shm reader
// (the newest len(dst) when more are pending).
type Source struct {
	mu      sync.Mutex
	gen     func(dst []api.Sample) (int, api.PanelSnapshot, bool)
	pending []api.Sample
	snap    api.PanelSnapshot
	ok      bool
	reads   int
}

// NewSource returns an attached source with an empty snapshot.
func NewSource() *Source { return &Source{ok: true} }

// ReadFrame implements api.PanelSource.
func (s *Source) ReadFrame(dst []api.Sample) (int, api.PanelSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.gen != nil {
		return s.gen(dst)
	}
	if !s.ok {
		return 0, api.PanelSnapshot{}, false
	}
	p := s.pending
	if len(p) > len(dst) {
		p = p[len(p)-len(dst):]
	}
	n := copy(dst, p)
	s.pending = s.pending[:0]
	return n, s.snap, true
}

// SetGen replaces the queue by a generator: ReadFrame returns what gen
// produces (called with the source's lock held, from the reader).
func (s *Source) SetGen(gen func(dst []api.Sample) (int, api.PanelSnapshot, bool)) {
	s.mu.Lock()
	s.gen = gen
	s.mu.Unlock()
}

// Push queues samples for the next ReadFrame.
func (s *Source) Push(samples ...api.Sample) {
	s.mu.Lock()
	s.pending = append(s.pending, samples...)
	s.mu.Unlock()
}

// SetSnapshot sets the snapshot ReadFrame returns.
func (s *Source) SetSnapshot(snap api.PanelSnapshot) {
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
}

// Update changes the snapshot in place.
func (s *Source) Update(fn func(*api.PanelSnapshot)) {
	s.mu.Lock()
	fn(&s.snap)
	s.mu.Unlock()
}

// SetOK sets whether an export is attached.
func (s *Source) SetOK(ok bool) {
	s.mu.Lock()
	s.ok = ok
	s.mu.Unlock()
}

// Reads returns the number of ReadFrame calls.
func (s *Source) Reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

var _ api.PanelSource = (*Source)(nil)
