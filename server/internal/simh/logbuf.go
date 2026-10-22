package simh

import (
	"strings"
	"sync"

	"pdp11emu/server/internal/api"
)

// logBuffer keeps the most recent simulator output lines (and the SCP
// commands pdp11d sent, as "sim> cmd"). It implements api.LogBuffer.
type logBuffer struct {
	mu     sync.Mutex
	ring   []string
	start  int // index of the oldest line
	n      int // lines stored
	subs   []logSub
	nextID uint64
}

type logSub struct {
	id    uint64
	fn    func(string)
	batch func([]string) // SubscribeHistory subscribers
}

func newLogBuffer(capacity int) *logBuffer {
	if capacity <= 0 {
		capacity = 2000
	}
	return &logBuffer{ring: make([]string, capacity)}
}

// add appends a line and passes it to the subscribers. Subscribers are
// called with the buffer locked (that keeps their view in order with
// Lines); they must not block or call back into the buffer.
func (b *logBuffer) add(line string) {
	line = strings.ToValidUTF8(line, "�")
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.n < len(b.ring) {
		b.ring[(b.start+b.n)%len(b.ring)] = line
		b.n++
	} else {
		b.ring[b.start] = line
		b.start = (b.start + 1) % len(b.ring)
	}
	var one [1]string
	for _, s := range b.subs {
		if s.batch != nil {
			one[0] = line
			s.batch(one[:])
		} else {
			s.fn(line)
		}
	}
}

// Lines returns up to n of the most recent lines, oldest first (n <= 0: all).
func (b *logBuffer) Lines(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.linesLocked(n)
}

func (b *logBuffer) linesLocked(n int) []string {
	if n <= 0 || n > b.n {
		n = b.n
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = b.ring[(b.start+b.n-n+i)%len(b.ring)]
	}
	return out
}

// Subscribe registers fn for every line added from now on.
func (b *logBuffer) Subscribe(fn func(line string)) (cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.subscribeLocked(logSub{fn: fn})
}

// SubscribeHistory returns up to n recent lines and registers fn for the
// lines that follow them, atomically (both under the buffer's lock, which
// add holds while it calls the subscribers).
func (b *logBuffer) SubscribeHistory(n int, fn func(lines []string)) (history []string, cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.linesLocked(n), b.subscribeLocked(logSub{batch: fn})
}

// subscribeLocked registers s. Caller holds b.mu.
func (b *logBuffer) subscribeLocked(s logSub) (cancel func()) {
	id := b.nextID
	b.nextID++
	s.id = id
	b.subs = append(b.subs, s)
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, s := range b.subs {
			if s.id == id {
				b.subs = append(b.subs[:i:i], b.subs[i+1:]...)
				return
			}
		}
	}
}

var _ api.LogBuffer = (*logBuffer)(nil)
