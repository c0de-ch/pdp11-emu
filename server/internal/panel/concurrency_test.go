package panel

import (
	"context"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel/paneltest"
)

func newTestLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{w: w}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestConcurrentInputs hammers the panel from many clients while the
// machine changes state on its own and frames are computed; run with -race.
// Input must never block, and the panel must settle afterwards.
func TestConcurrentInputs(t *testing.T) {
	e := runningEnv(t)
	e.m.Lock()
	e.m.PowerOnDelay = 2 * time.Millisecond
	e.m.Unlock()
	shell(e, time.Millisecond)
	rng := rand.New(rand.NewPCG(7, 7))
	e.src.SetGen(func(dst []api.Sample) (int, api.PanelSnapshot, bool) {
		n := 50
		for i := 0; i < n; i++ {
			dst[i] = api.Sample{Addr: rng.Uint32(), Data: uint16(rng.Uint32()), VA: uint16(rng.Uint32()), Weight: 1 + rng.Uint32N(100)}
		}
		return n, api.PanelSnapshot{TrapCount: rng.Uint32N(3), DR: uint16(rng.Uint32())}, true
	})

	var slow atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(g), 1))
			c := uint64(g + 1)
			for i := 0; i < 400; i++ {
				var in api.PanelInput
				switch r.IntN(10) {
				case 0:
					in = api.PanelInput{Kind: "sr", Int: r.IntN(srMask + 1)}
				case 1, 2:
					names := []string{swLoadAdrs, swExam, swDep, swCont, swStart, swLampTest}
					in = api.PanelInput{Kind: "momentary", Name: names[r.IntN(len(names))], Active: r.IntN(2) == 0}
				case 3:
					in = api.PanelInput{Kind: "latch", Name: "ENABLE_HALT", Value: []string{"enable", "halt"}[r.IntN(2)]}
				case 4:
					in = api.PanelInput{Kind: "latch", Name: "SINST_SBUS", Value: []string{"inst", "bus"}[r.IntN(2)]}
				case 5:
					in = api.PanelInput{Kind: "knob", Name: []string{"addr", "data"}[r.IntN(2)], Int: r.IntN(8)}
				case 6:
					in = api.PanelInput{Kind: "key", Value: []string{keyOff, keyPower, keyLock}[r.IntN(3)]}
				case 7:
					in = api.PanelInput{Kind: []string{"boot", "shutdownOS", "retryPower"}[r.IntN(3)]}
				case 8:
					e.p.ReleaseClient(c)
					_ = e.p.Controls()
					_ = e.p.Console()
					_ = e.p.PowerStatus()
					continue
				case 9:
					time.Sleep(time.Duration(r.IntN(200)) * time.Microsecond)
					continue
				}
				in.Client = c
				start := time.Now()
				e.p.Input(in)
				if d := time.Since(start); d > 50*time.Millisecond {
					slow.Add(1)
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() { // the machine does things on its own
		defer wg.Done()
		r := rand.New(rand.NewPCG(9, 9))
		for {
			select {
			case <-stop:
				return
			default:
			}
			switch r.IntN(3) {
			case 0:
				e.m.StopCPU("HALT instruction", uint16(r.Uint32()))
			case 1:
				e.m.Stream().EmitString("# ")
			case 2:
				e.m.SetStats(api.MachineStats{MIPS: r.Float64()})
			}
			time.Sleep(time.Millisecond)
		}
	}()
	done := make(chan struct{})
	go func() {
		time.Sleep(2 * time.Second)
		close(stop)
	}()
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("inputs did not finish (deadlock?)")
	}
	if n := slow.Load(); n > 0 {
		t.Errorf("Input blocked for more than 50 ms %d times", n)
	}
	for c := uint64(1); c <= 8; c++ {
		e.p.ReleaseClient(c)
	}
	e.idle()
	ctl := e.p.Controls()
	for name, down := range ctl.Pressed {
		if down {
			t.Errorf("%s still pressed after every client left", name)
		}
	}
	if ctl.LampTest {
		t.Error("lamp test still active after every client left")
	}
	// The panel still works.
	e.do("key off")
	e.do("enable")
	e.do("key power")
	if st := e.m.State(); st != api.RunRunning {
		t.Fatalf("state %s after the storm", st)
	}
}

// TestSubscribers checks registration and cancellation while delivering.
func TestSubscribers(t *testing.T) {
	var s subscribers[func(int)]
	var a, b atomic.Int64
	ca := s.add(func(v int) { a.Add(int64(v)) })
	cb := s.add(func(v int) { b.Add(int64(v)) })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				s.each(func(fn func(int)) { fn(1) })
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			c := s.add(func(int) {})
			c()
			c() // idempotent
		}
	}()
	wg.Wait()
	if a.Load() != 4000 || b.Load() != 4000 {
		t.Fatalf("deliveries a=%d b=%d", a.Load(), b.Load())
	}
	ca()
	s.each(func(fn func(int)) { fn(1) })
	if a.Load() != 4000 || b.Load() != 4001 || s.len() != 1 {
		t.Fatalf("after cancel a=%d b=%d len=%d", a.Load(), b.Load(), s.len())
	}
	cb()
	if s.len() != 0 {
		t.Fatal("not empty")
	}
}

// TestRunTwice guards the lifecycle.
func TestRunTwice(t *testing.T) {
	e := newEnv(t)
	if err := e.p.Run(context.Background()); err == nil {
		t.Fatal("second Run accepted")
	}
	if _, err := New(nil, nil, nil, Options{}); err == nil {
		t.Fatal("New without a machine accepted")
	}
	m := paneltest.NewMachine()
	defer m.Close()
	p, err := New(m, nil, paneltest.DefaultSystems(), Options{FrameHz: 100000, Logger: quietLogger()})
	if err != nil || p.opts.FrameHz != 1000 {
		t.Fatalf("FrameHz clamp: %v %d", err, p.opts.FrameHz)
	}
}
