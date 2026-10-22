package panel

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel/paneltest"
)

// shortBootWatch makes BootTimeoutSec count in units of d (bsd211 of the
// fake catalog has 90).
func shortBootWatch(t *testing.T, d time.Duration) {
	old := bootWatchUnit
	bootWatchUnit = d
	t.Cleanup(func() { bootWatchUnit = old })
}

const bsdBootWarning = "warn:2.11BSD has not reached its prompt after 90 s — check the console. If it hangs, power off and boot again."

func (e *env) bootWarnings() []string {
	var out []string
	for _, ts := range e.toastList() {
		if strings.Contains(ts, "has not reached its prompt") {
			out = append(out, ts)
		}
	}
	return out
}

// awaitBootWarning waits for the warning and checks that no second one
// follows.
func (e *env) awaitBootWarning() {
	e.t.Helper()
	if !paneltest.WaitFor(5*time.Second, func() bool { return len(e.bootWarnings()) > 0 }) {
		e.t.Fatalf("no boot warning; toasts %q", e.toastList())
	}
	time.Sleep(200 * time.Millisecond)
	if got := e.bootWarnings(); len(got) != 1 || got[0] != bsdBootWarning {
		e.t.Fatalf("boot warnings %q, want one %q", got, bsdBootWarning)
	}
}

// watch returns the current boot watch (after the pending Machine events)
// and whether its timer runs.
func (e *env) watch() (*bootWatch, bool) {
	e.m.Sync()
	e.p.mu.Lock()
	defer e.p.mu.Unlock()
	return e.p.bootWatch, e.p.bootWatch != nil && e.p.bootWatch.timer != nil
}

func TestBootTimeoutWarning(t *testing.T) {
	t.Run("autostart boot without the prompt warns everyone once", func(t *testing.T) {
		shortBootWatch(t, time.Millisecond) // 90 ms
		e := runningEnv(t)
		e.awaitBootWarning()
		if bw, _ := e.watch(); bw != nil {
			t.Fatal("watch still registered after the warning")
		}
	})
	for _, tc := range []struct {
		name string
		do   func(e *env)
	}{
		{"the prompt", func(e *env) { e.m.SetReady(true) }},
		{"a stop", func(e *env) { e.do("halt") }},
		{"power off", func(e *env) { e.do("key off") }},
	} {
		t.Run(tc.name+" cancels the warning", func(t *testing.T) {
			e := runningEnv(t) // 90 s: the timer cannot fire during the test
			if _, armed := e.watch(); !armed {
				t.Fatal("no boot watch while booting")
			}
			tc.do(e)
			if bw, _ := e.watch(); bw != nil {
				t.Fatalf("watch survived %s", tc.name)
			}
		})
	}
	t.Run("a new boot replaces the watch; its restart only pauses it", func(t *testing.T) {
		e := runningEnv(t)
		first, _ := e.watch()
		// Toolbar boot of a running CPU: the simulator restarts (starting →
		// halted → running).
		e.do("boot")
		if !slices.Contains(e.m.CallNames(), "Boot") {
			t.Fatalf("calls %q", e.m.CallNames())
		}
		second, armed := e.watch()
		if second == nil || second == first || !armed || second.armed != 1 {
			t.Fatalf("after the reboot: watch %p (first %p), armed %v", second, first, armed)
		}
		e.p.mu.Lock()
		stopped := first.timer == nil
		e.p.mu.Unlock()
		if !stopped {
			t.Fatal("the first boot's timer still runs")
		}
	})
	t.Run("the toast carries the boot's req", func(t *testing.T) {
		shortBootWatch(t, time.Millisecond)
		e := haltedEnv(t) // powered on halted: nothing to watch
		if bw, _ := e.watch(); bw != nil {
			t.Fatal("power-on without boot is watched")
		}
		var mu sync.Mutex
		var reqs []string
		e.p.Toasts(func(level, text, req string) {
			mu.Lock()
			reqs = append(reqs, req)
			mu.Unlock()
		})
		e.do("enable")
		if h := e.input(api.PanelInput{Kind: "boot", Req: "b1"}); h != "" {
			t.Fatalf("boot: hint %q", h)
		}
		e.awaitBootWarning()
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(reqs, []string{"b1"}) {
			t.Fatalf("toast reqs %q", reqs)
		}
	})
	t.Run("START at the boot ROM is watched", func(t *testing.T) {
		shortBootWatch(t, time.Millisecond)
		e := haltedEnv(t)
		e.do("sr 165000")
		e.do("load")
		e.do("enable")
		e.do("start")
		if !slices.Contains(e.m.CallNames(), "Boot") {
			t.Fatalf("calls %q", e.m.CallNames())
		}
		e.awaitBootWarning()
	})
	t.Run("no watch without a boot timeout or after a failed boot", func(t *testing.T) {
		e := newEnv(t, notStarted())
		e.sys.SetBootTimeout("bsd211", 0)
		e.start()
		if st := e.m.State(); st != api.RunRunning {
			t.Fatalf("state %s", st)
		}
		if bw, _ := e.watch(); bw != nil {
			t.Fatal("watched without a boot timeout")
		}

		f := newEnv(t, notStarted())
		f.m.Lock()
		f.m.PowerOnErr = errors.New("simulator crashed")
		f.m.Unlock()
		f.start()
		if st := f.m.State(); st != api.RunError {
			t.Fatalf("state %s", st)
		}
		if bw, _ := f.watch(); bw != nil {
			t.Fatal("watch after a failed power-on")
		}
	})
}
