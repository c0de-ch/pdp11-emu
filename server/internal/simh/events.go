package simh

import (
	"log/slog"
	"sync"

	"pdp11emu/server/internal/api"
)

// eventBus delivers machine events in order on one goroutine, never while
// the emitter's locks are held: emit only appends to a queue.
type eventBus struct {
	log *slog.Logger

	mu     sync.Mutex
	queue  []api.MachineEvent
	subs   []subscriber // in subscription order
	nextID uint64
	closed bool

	wake chan struct{}
	done chan struct{}
}

func newEventBus(log *slog.Logger) *eventBus {
	b := &eventBus{
		log:  log,
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *eventBus) emit(ev api.MachineEvent) {
	b.mu.Lock()
	if !b.closed {
		b.queue = append(b.queue, ev)
	}
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *eventBus) subscribe(fn func(api.MachineEvent)) (cancel func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	b.subs = append(b.subs, subscriber{id, fn})
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

type subscriber struct {
	id uint64
	fn func(api.MachineEvent)
}

// close stops delivery after the queued events have been delivered.
func (b *eventBus) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	<-b.done
}

func (b *eventBus) run() {
	defer close(b.done)
	for range b.wake {
		for {
			b.mu.Lock()
			if len(b.queue) == 0 {
				closed := b.closed
				b.mu.Unlock()
				if closed {
					return
				}
				break
			}
			ev := b.queue[0]
			b.queue[0] = api.MachineEvent{}
			b.queue = b.queue[1:]
			subs := b.subs // copy-on-write: cancel never mutates a published slice
			b.mu.Unlock()
			for _, s := range subs {
				b.deliver(s.fn, ev)
			}
		}
	}
}

// deliver calls one subscriber; a panicking subscriber is logged rather than
// allowed to take the simulator (and the guest's state) down with pdp11d.
func (b *eventBus) deliver(fn func(api.MachineEvent), ev api.MachineEvent) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("machine event subscriber panicked", "panic", r, "event", ev.Kind)
		}
	}()
	fn(ev)
}
