package paneltest

import (
	"sync"

	"pdp11emu/server/internal/api"
)

// Log is a fake api.LogBuffer. Subscribers are called in order, outside
// the lock that guards the lines but before the next line is stored, so
// SubscribeHistory is atomic with respect to Add.
type Log struct {
	deliver sync.Mutex // held while subscribers are called: keeps lines in order
	mu      sync.Mutex
	lines   []string
	subs    map[int]func([]string)
	next    int
}

// NewLog returns an empty log.
func NewLog() *Log { return &Log{subs: map[int]func([]string){}} }

// Add appends a line and notifies the subscribers.
func (l *Log) Add(line string) {
	l.deliver.Lock()
	defer l.deliver.Unlock()
	l.mu.Lock()
	l.lines = append(l.lines, line)
	if len(l.lines) > 2000 {
		l.lines = l.lines[len(l.lines)-2000:]
	}
	subs := make([]func([]string), 0, len(l.subs))
	for _, fn := range l.subs {
		subs = append(subs, fn)
	}
	l.mu.Unlock()
	for _, fn := range subs {
		fn([]string{line})
	}
}

func (l *Log) Lines(n int) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.linesLocked(n)
}

func (l *Log) linesLocked(n int) []string {
	if n > len(l.lines) || n <= 0 {
		n = len(l.lines)
	}
	return append([]string(nil), l.lines[len(l.lines)-n:]...)
}

func (l *Log) Subscribe(fn func(line string)) (cancel func()) {
	return l.subscribe(func(lines []string) {
		for _, line := range lines {
			fn(line)
		}
	})
}

// SubscribeHistory implements api.LogBuffer: the snapshot and the
// registration happen under the same lock Add stores lines under.
func (l *Log) SubscribeHistory(n int, fn func(lines []string)) (history []string, cancel func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.linesLocked(n), l.subscribeLocked(fn)
}

func (l *Log) subscribe(fn func([]string)) (cancel func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.subscribeLocked(fn)
}

func (l *Log) subscribeLocked(fn func([]string)) (cancel func()) {
	l.next++
	id := l.next
	l.subs[id] = fn
	return func() { l.mu.Lock(); delete(l.subs, id); l.mu.Unlock() }
}

var _ api.LogBuffer = (*Log)(nil)
