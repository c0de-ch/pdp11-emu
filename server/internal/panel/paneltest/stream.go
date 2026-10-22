package paneltest

import (
	"sync"

	"pdp11emu/server/internal/api"
)

// HistorySize is the console history kept by the fake (as the real one).
const HistorySize = 256 << 10

// InputLimit is the input queue limit (as the real one).
const InputLimit = 64 << 10

// Stream is a fake api.Stream. Output is produced with Emit; subscribers
// are called under the stream's lock (so SubscribeHistory is atomic with
// respect to Emit) with a scratch buffer that is overwritten after the
// call, which catches subscribers that keep it.
type Stream struct {
	// OnWrite, if set, is called (outside the lock) with every accepted
	// input, e.g. to emulate a shell echoing and prompting.
	OnWrite func(p []byte)

	mu        sync.Mutex
	hist      []byte
	subs      map[int]func([]byte)
	next      int
	connected bool
	input     []byte
	queued    int
	cancels   int
	scratch   []byte
}

// NewStream returns a disconnected stream.
func NewStream() *Stream { return &Stream{subs: map[int]func([]byte){}} }

// Write implements api.Stream.
func (s *Stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	if !s.connected {
		s.mu.Unlock()
		return 0, api.ErrNotPowered
	}
	if s.queued+len(p) > InputLimit {
		s.mu.Unlock()
		return 0, api.ErrInputFull
	}
	s.input = append(s.input, p...)
	cb := s.OnWrite
	s.mu.Unlock()
	if cb != nil {
		cb(append([]byte(nil), p...))
	}
	return len(p), nil
}

// SubscribeHistory implements api.Stream.
func (s *Stream) SubscribeHistory(fn func(p []byte)) (history []byte, cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	s.subs[id] = fn
	return append([]byte(nil), s.hist...), func() {
		s.mu.Lock()
		delete(s.subs, id)
		s.mu.Unlock()
	}
}

func (s *Stream) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

func (s *Stream) Queued() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queued
}

func (s *Stream) CancelInput() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = 0
	s.cancels++
}

// --- test helpers ---

// Emit produces console output (as if the guest printed p).
func (s *Stream) Emit(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hist = append(s.hist, p...)
	if over := len(s.hist) - HistorySize; over > 0 {
		s.hist = append(s.hist[:0], s.hist[over:]...)
	}
	s.scratch = append(s.scratch[:0], p...)
	for _, fn := range s.subs {
		fn(s.scratch)
	}
	for i := range s.scratch {
		s.scratch[i] = 0xEE // the buffer is only valid during the call
	}
}

// EmitString is Emit for text.
func (s *Stream) EmitString(text string) { s.Emit([]byte(text)) }

// SetConnected sets what Connected returns (Write fails while false).
func (s *Stream) SetConnected(c bool) {
	s.mu.Lock()
	s.connected = c
	s.mu.Unlock()
}

// SetQueued sets what Queued returns.
func (s *Stream) SetQueued(n int) {
	s.mu.Lock()
	s.queued = n
	s.mu.Unlock()
}

// Input returns all input written so far.
func (s *Stream) Input() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.input...)
}

// Cancels returns how often CancelInput was called.
func (s *Stream) Cancels() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancels
}

// Subscribers returns the number of output subscribers.
func (s *Stream) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

var _ api.Stream = (*Stream)(nil)
