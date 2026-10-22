package panel

import (
	"sync"
	"sync/atomic"
)

// subscribers is a registry of callbacks. Delivery (each) takes no lock and
// does not allocate: the registered functions are kept in an immutable
// slice that add/cancel replace (copy on write). A callback may still be
// called once after its cancel function returned, if a delivery that
// started earlier is still running.
type subscribers[F any] struct {
	mu   sync.Mutex
	next uint64
	ids  []uint64
	fns  atomic.Pointer[[]F]
	all  []F
}

func (s *subscribers[F]) add(fn F) (cancel func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	s.ids = append(s.ids, id)
	s.all = append(s.all, fn)
	s.publish()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for i, v := range s.ids {
				if v == id {
					s.ids = append(s.ids[:i:i], s.ids[i+1:]...)
					s.all = append(s.all[:i:i], s.all[i+1:]...)
					break
				}
			}
			s.publish()
		})
	}
}

// publish stores a private copy of the current callbacks. Caller holds mu.
func (s *subscribers[F]) publish() {
	cp := make([]F, len(s.all))
	copy(cp, s.all)
	s.fns.Store(&cp)
}

// each calls fn for every registered callback.
func (s *subscribers[F]) each(call func(F)) {
	p := s.fns.Load()
	if p == nil {
		return
	}
	for _, fn := range *p {
		call(fn)
	}
}

// len returns the number of registered callbacks.
func (s *subscribers[F]) len() int {
	p := s.fns.Load()
	if p == nil {
		return 0
	}
	return len(*p)
}
