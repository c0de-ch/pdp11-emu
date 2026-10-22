package simh

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

func TestLogBufferRing(t *testing.T) {
	b := newLogBuffer(3)
	for i := 0; i < 5; i++ {
		b.add(fmt.Sprintf("l%d", i))
	}
	if got := b.Lines(0); !slices.Equal(got, []string{"l2", "l3", "l4"}) {
		t.Fatalf("Lines(0) = %q", got)
	}
	if got := b.Lines(2); !slices.Equal(got, []string{"l3", "l4"}) {
		t.Fatalf("Lines(2) = %q", got)
	}
	hist, cancel := b.SubscribeHistory(2, func([]string) {})
	cancel()
	if !slices.Equal(hist, []string{"l3", "l4"}) {
		t.Fatalf("history %q", hist)
	}
	b.add("bad \xff utf-8")
	if got := b.Lines(1)[0]; got != "bad � utf-8" {
		t.Fatalf("invalid UTF-8 kept: %q", got)
	}
}

// TestLogBufferSubscribeHistory: subscriptions taken while lines are being
// added see every line exactly once, in order (history, then live), also
// when the lines are identical.
func TestLogBufferSubscribeHistory(t *testing.T) {
	const total = 20000
	b := newLogBuffer(total)
	type view struct {
		mu   sync.Mutex
		hist []string
		live []string
	}
	var views []*view
	var cancels []func()
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < total; i++ {
			b.add(fmt.Sprintf("line %d", i))
		}
	}()
	for i := 0; i < 50; i++ {
		v := &view{}
		v.mu.Lock()
		hist, cancel := b.SubscribeHistory(0, func(lines []string) {
			v.mu.Lock()
			v.live = append(v.live, lines...)
			v.mu.Unlock()
		})
		v.hist = hist
		v.mu.Unlock()
		mu.Lock()
		views = append(views, v)
		cancels = append(cancels, cancel)
		mu.Unlock()
	}
	<-done
	for _, c := range cancels {
		c()
	}
	for i, v := range views {
		all := append(slices.Clone(v.hist), v.live...)
		if len(all) != total {
			t.Fatalf("view %d: %d lines (%d history + %d live), want %d", i, len(all), len(v.hist), len(v.live), total)
		}
		for j, l := range all {
			if want := fmt.Sprintf("line %d", j); l != want {
				t.Fatalf("view %d line %d = %q, want %q", i, j, l, want)
			}
		}
	}

	// Identical lines: counts must still add up.
	b = newLogBuffer(total)
	var live int
	var lmu sync.Mutex
	done = make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 5000; i++ {
			b.add("same")
		}
	}()
	hist, cancel := b.SubscribeHistory(0, func(lines []string) {
		lmu.Lock()
		live += len(lines)
		lmu.Unlock()
	})
	<-done
	cancel()
	lmu.Lock()
	defer lmu.Unlock()
	if len(hist)+live != 5000 {
		t.Fatalf("%d history + %d live lines, want 5000", len(hist), live)
	}
}
