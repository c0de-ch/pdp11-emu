package shm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
)

func openHeap(t *testing.T, ringWr uint32) (*producer, *Reader) {
	t.Helper()
	mem := alignedBuffer()
	p := newProducer(mem, ringWr)
	r, err := newReader("heap", mem, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

func checkSamples(t *testing.T, got []api.Sample, first uint32) {
	t.Helper()
	for i, s := range got {
		k := first + uint32(i)
		addr, data, va, weight, pc, ir := expected(k)
		want := api.Sample{Addr: addr, Data: data, VA: va, Weight: weight, PC: pc, IR: ir}
		if s != want {
			t.Fatalf("sample %d (k=%d): got %+v, want %+v", i, k, s, want)
		}
	}
}

func TestOpenValidates(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int, mutate func(p *producer)) string {
		path := filepath.Join(dir, name)
		buf := alignedBuffer()
		p := newProducer(buf, 0)
		if mutate != nil {
			mutate(p)
		}
		if size > len(buf) {
			buf = append(buf, make([]byte, size-len(buf))...)
		}
		if err := os.WriteFile(path, buf[:size], 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"ok", write("ok", FileSize, nil), ""},
		{"missing", filepath.Join(dir, "nope"), "no such file"},
		{"short", write("short", FileSize-16, nil), "file size"},
		{"long", write("long", FileSize+4096, nil), "file size"},
		{"magic", write("magic", FileSize, func(p *producer) { p.store(OffMagic, 0x50313151) }), "magic"},
		{"version1", write("v1", FileSize, func(p *producer) { p.store(OffVersion, 1) }), "version 1"},
		{"version2", write("v2", FileSize, func(p *producer) { p.store(OffVersion, 2) }), "layout version 2 (the simulator and pdp11d come from different builds)"},
		{"sizefield", write("sizef", FileSize, func(p *producer) { p.store(OffSize, 32952) }), "header size"},
		{"ringsize", write("ring", FileSize, func(p *producer) { p.store(OffRingSize, 4096) }), "ring size"},
		{"zeroed", write("zero", FileSize, func(p *producer) {
			for i := range p.mem {
				p.mem[i] = 0
			}
		}), "magic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Open(tc.path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				r.Close()
				t.Fatalf("Open succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
			if tc.name != "missing" && !errors.Is(err, ErrFormat) {
				t.Fatalf("error %v is not ErrFormat", err)
			}
		})
	}
}

// TestOpenRefusesUnsafeFiles: like ATTACH PANEL, Open maps only a regular
// file of this user that is closed to group and others, never through a
// symbolic link.
func TestOpenRefusesUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel")
	newProducer(mappedFile(t, path), 0)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	open := func(p string) error {
		r, err := Open(p)
		if err == nil {
			r.Close()
		}
		return err
	}
	if err := open(path); err != nil {
		t.Fatalf("Open of a 0600 file: %v", err)
	}
	for _, tc := range []struct {
		name, path, want string
	}{
		{"symbolic link", link, "is a symbolic link"},
		{"FIFO", fifo, "is not a regular file"},
	} {
		if err := open(tc.path); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.want)
		}
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o620, 0o602, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("is open to other users (mode %04o; it must be 0600)", mode)
		if err := open(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("mode %04o: error %v, want %q", mode, err, want)
		}
	}
	// Another user's file (checkFile with a different effective uid).
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	euid := os.Geteuid()
	want := fmt.Sprintf("belongs to uid %d, not to this user (uid %d)", euid, euid+1)
	if err := checkFile(path, st, euid+1); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("foreign owner: error %v, want %q", err, want)
	}
}

// TestSharedMapping exercises the real Open path: the producer maps the
// file like the simulator does, the reader maps it separately.
func TestSharedMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel")
	p := newProducer(mappedFile(t, path), 100)
	p.write(5) // before Open: history, not returned
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p.write(3)
	p.block(42)
	p.store(OffState, api.PnlWaiting)
	p.store(OffTrapCount, 9)
	p.store(OffStopCount, 4)
	for i := 0; i < 16; i++ {
		p.store(OffDROn+4*i, uint32(1000*i+7))
	}
	p.store(OffWeightSum, 0xFFFFFFF0)

	dst := make([]api.Sample, 16)
	n, snap, ok := r.ReadFrame(dst)
	if !ok || n != 3 {
		t.Fatalf("ReadFrame = %d, ok=%v; want 3 samples", n, ok)
	}
	checkSamples(t, dst[:n], 105)
	if g := checkBlock(t, snap); g != 42 {
		t.Fatalf("generation %d", g)
	}
	if snap.State != api.PnlWaiting || snap.TrapCount != 9 || snap.StopCount != 4 || snap.RingWr != 108 {
		t.Fatalf("status words: %+v", snap)
	}
	for i, v := range snap.DROn {
		if v != uint32(1000*i+7) {
			t.Fatalf("DROn[%d] = %d", i, v)
		}
	}
	if snap.WeightSum != 0xFFFFFFF0 {
		t.Fatalf("WeightSum = %#x", snap.WeightSum)
	}

	r.SetSR(0o123456)
	if p.load(OffSR) != 0o123456 || p.load(OffSRValid) != 1 {
		t.Fatalf("sr=%o valid=%d", p.load(OffSR), p.load(OffSRValid))
	}
	r.SetTargetRate(25000)
	if p.load(OffTargetRate) != 25000 {
		t.Fatal("target_rate not written")
	}
}

func TestReadFrame(t *testing.T) {
	tests := []struct {
		name      string
		ringWr    uint32 // initial ring_wr
		write     int    // samples written before the read
		dst       int
		wantN     int
		wantFirst uint32 // sequence number of the first returned sample
		wantDrop  uint64
	}{
		{"empty", 0, 0, 64, 0, 0, 0},
		{"some", 0, 10, 64, 10, 0, 0},
		{"dst limits to newest", 0, 100, 64, 64, 36, 36},
		{"exactly half ring", 0, RingSize / 2, RingSize, RingSize / 2, 0, 0},
		{"overrun skips ahead", 0, RingSize/2 + 10, RingSize, RingSize / 2, 10, 10},
		{"lapped ring", 0, 3*RingSize + 5, RingSize, RingSize / 2, 3*RingSize + 5 - RingSize/2, 3*RingSize + 5 - RingSize/2},
		{"ring_wr wraps 2^32", 0xFFFFFFF0, 40, 64, 40, 0xFFFFFFF0, 0},
		{"wrap plus overrun", 0xFFFFF000, RingSize, RingSize, RingSize / 2, 0x1000 /* 0xFFFFF000+RingSize/2 mod 2^32 */, RingSize / 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, r := openHeap(t, tc.ringWr)
			p.write(tc.write)
			dst := make([]api.Sample, tc.dst)
			n, snap, ok := r.ReadFrame(dst)
			if !ok {
				t.Fatal("not ok")
			}
			if n != tc.wantN {
				t.Fatalf("n = %d, want %d", n, tc.wantN)
			}
			checkSamples(t, dst[:n], tc.wantFirst)
			if r.Dropped() != tc.wantDrop {
				t.Fatalf("dropped = %d, want %d", r.Dropped(), tc.wantDrop)
			}
			if snap.RingWr != tc.ringWr+uint32(tc.write) {
				t.Fatalf("RingWr = %d", snap.RingWr)
			}
			// Everything pending was consumed: the next read is empty,
			// then new samples follow on seamlessly.
			if n, _, _ := r.ReadFrame(dst); n != 0 {
				t.Fatalf("second read returned %d samples", n)
			}
			p.write(3)
			n, _, _ = r.ReadFrame(dst)
			if n != 3 {
				t.Fatalf("third read returned %d samples", n)
			}
			checkSamples(t, dst[:n], tc.ringWr+uint32(tc.write))
		})
	}
}

// TestConcurrentProducer runs a producer goroutine against the reader on a
// heap buffer (visible to the race detector): every sample must be intact
// and in order, every snapshot a single generation.
func TestConcurrentProducer(t *testing.T) {
	p, r := openHeap(t, 0xFFFF0000) // cross the 2^32 wrap during the run
	stop := make(chan struct{})
	var wg sync.WaitGroup
	stopProducer := sync.OnceFunc(func() { close(stop); wg.Wait() })
	defer stopProducer() // also when the test fails: never leave it spinning
	wg.Add(1)
	go func() {
		defer wg.Done()
		g := uint32(0)
		for {
			select {
			case <-stop:
				return
			default:
			}
			p.write(37)
			g++
			p.block(g)
			p.serviceHalt()
		}
	}()

	dst := make([]api.Sample, 4096)
	var got, frames int
	prev := uint32(0)
	havePrev := false
	lastGen := uint32(0)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		n, snap, ok := r.ReadFrame(dst)
		if !ok {
			t.Fatal("not ok")
		}
		for i := 0; i < n; i++ {
			k := dst[i].Addr
			addr, data, va, weight, pc, ir := expected(k)
			if dst[i] != (api.Sample{Addr: addr, Data: data, VA: va, Weight: weight, PC: pc, IR: ir}) {
				t.Fatalf("torn sample %+v", dst[i])
			}
			// k must follow prev (mod 2^32); skipping ahead is allowed.
			if d := k - prev; havePrev && (d == 0 || d >= 1<<31) {
				t.Fatalf("sample %d after %d: out of order", k, prev)
			}
			prev, havePrev = k, true
		}
		if snap.SimTime != 0 {
			g := checkBlock(t, snap)
			if g < lastGen {
				t.Fatalf("generation went backwards: %d after %d", g, lastGen)
			}
			lastGen = g
		}
		got += n
		frames++
		if frames%4 == 0 {
			r.RequestHalt()
		}
	}
	stopProducer()
	if got == 0 || lastGen == 0 {
		t.Fatalf("no data: %d samples, generation %d", got, lastGen)
	}
	t.Logf("%d samples in %d frames, %d dropped, generation %d", got, frames, r.Dropped(), lastGen)
}

func TestHaltHandshake(t *testing.T) {
	p, r := openHeap(t, 0)
	p.store(OffHaltAck, 41)
	p.store(OffHaltReq, 41)

	// Request, honoured by the simulator.
	r.RequestHalt()
	if got := p.load(OffHaltReq); got != 42 {
		t.Fatalf("halt_req = %d, want 42", got)
	}
	if !r.HaltPending() {
		t.Fatal("request not pending")
	}
	if !p.serviceHalt() || r.HaltAck() != 42 || r.HaltPending() {
		t.Fatalf("ack = %d, pending = %v", r.HaltAck(), r.HaltPending())
	}

	// A request that raced a natural stop is cancelled before the next run,
	// so the simulator never sees it.
	r.RequestHalt()
	r.CancelHalt()
	if p.serviceHalt() {
		t.Fatal("cancelled request was honoured")
	}
	if p.load(OffHaltReq) != 42 || r.HaltPending() {
		t.Fatalf("halt_req = %d after cancel", p.load(OffHaltReq))
	}

	// Repeated requests do not accumulate: req is always ack+1.
	r.RequestHalt()
	r.RequestHalt()
	if p.load(OffHaltReq) != 43 {
		t.Fatalf("halt_req = %d, want 43", p.load(OffHaltReq))
	}
	// halt_ack wraps like any counter.
	p.store(OffHaltAck, 0xFFFFFFFF)
	r.RequestHalt()
	if p.load(OffHaltReq) != 0 {
		t.Fatalf("halt_req = %d, want 0 after wrap", p.load(OffHaltReq))
	}
}

func TestClose(t *testing.T) {
	p, r := openHeap(t, 0)
	p.write(10)
	var unmapped atomic.Int32
	r.unmap = func() error { unmapped.Add(1); return nil }

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst := make([]api.Sample, 8)
			for j := 0; j < 1000; j++ {
				r.ReadFrame(dst)
				r.SetSR(uint16(j))
				r.RequestHalt()
				r.CancelHalt()
				r.Snapshot()
			}
		}()
	}
	time.Sleep(time.Millisecond)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if unmapped.Load() != 1 {
		t.Fatalf("unmapped %d times", unmapped.Load())
	}
	if n, _, ok := r.ReadFrame(make([]api.Sample, 8)); ok || n != 0 {
		t.Fatal("ReadFrame after Close")
	}
	if _, ok := r.Snapshot(); ok {
		t.Fatal("Snapshot after Close")
	}
	r.SetSR(1) // must not panic
	r.SetTargetRate(1)
	if r.HaltPending() || r.HaltAck() != 0 {
		t.Fatal("halt state after Close")
	}
}

// TestSnapshotStuckWriter: a simulator that died inside the seqlock write
// section must not hang the reader; it gets the last consistent block.
func TestSnapshotStuckWriter(t *testing.T) {
	p, r := openHeap(t, 0)
	p.block(7)
	s, _ := r.Snapshot()
	checkBlock(t, s)
	p.store(OffSeq, p.load(OffSeq)+1) // odd forever
	p.store(OffPSW, 0o777)
	done := make(chan api.PanelSnapshot)
	go func() { s, _ := r.Snapshot(); done <- s }()
	select {
	case s := <-done:
		if g := checkBlock(t, s); g != 7 {
			t.Fatalf("generation %d", g)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Snapshot hangs on a stuck writer")
	}
}

// TestLayoutMatchesHeader compiles the canonical C header and compares its
// offsets with layout.go.
func TestLayoutMatchesHeader(t *testing.T) {
	header, err := filepath.Abs("../../../simh/panel/pdp11_panel_shm.h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(header); err != nil {
		t.Skipf("header not found: %v", err)
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "off.c")
	prog := `#include <stdio.h>
#include "` + header + `"
#define P(f) printf("%s %zu\n", #f, offsetof(PANEL_SHM, f))
int main(void) {
	P(magic); P(version); P(size); P(ring_size); P(ring_wr); P(state); P(seq);
	P(stop_count); P(r); P(psw); P(dr); P(mmr0); P(mmr3); P(last_pa); P(last_data);
	P(sim_time); P(sample_interval); P(samples_per_sec); P(sr); P(sr_valid);
	P(halt_req); P(target_rate); P(trap_count); P(halt_ack); P(pc_pa);
	P(instr_count); P(dr_on); P(weight_sum); P(ring);
	printf("sizeof_shm %zu\nsizeof_sample %zu\nconst_ring %u\nconst_magic %u\nconst_version %u\n",
		sizeof(PANEL_SHM), sizeof(PANEL_SAMPLE), PANEL_RING, PANEL_MAGIC, PANEL_VERSION);
	printf("s_addr %zu\ns_data %zu\ns_va %zu\ns_weight %zu\ns_pc %zu\ns_ir %zu\n",
		offsetof(PANEL_SAMPLE, addr), offsetof(PANEL_SAMPLE, data), offsetof(PANEL_SAMPLE, va),
		offsetof(PANEL_SAMPLE, weight), offsetof(PANEL_SAMPLE, pc), offsetof(PANEL_SAMPLE, ir));
	return 0;
}
`
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "off")
	if out, err := exec.Command(cc, "-std=c11", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, out)
	}
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		v, err := strconv.Atoi(f[1])
		if err != nil {
			t.Fatal(err)
		}
		got[f[0]] = v
	}
	want := map[string]int{
		"magic": OffMagic, "version": OffVersion, "size": OffSize, "ring_size": OffRingSize,
		"ring_wr": OffRingWr, "state": OffState, "seq": OffSeq, "stop_count": OffStopCount,
		"r": OffR, "psw": OffPSW, "dr": OffDR, "mmr0": OffMMR0, "mmr3": OffMMR3,
		"last_pa": OffLastPA, "last_data": OffLastData, "sim_time": OffSimTime,
		"sample_interval": OffSampleInterval, "samples_per_sec": OffSamplesPerSec,
		"sr": OffSR, "sr_valid": OffSRValid, "halt_req": OffHaltReq, "target_rate": OffTargetRate,
		"trap_count": OffTrapCount, "halt_ack": OffHaltAck, "pc_pa": OffPCPA,
		"instr_count": OffInstrCount, "dr_on": OffDROn, "weight_sum": OffWeightSum, "ring": OffRing,
		"sizeof_shm": FileSize, "sizeof_sample": SampleSize, "const_ring": RingSize,
		"const_magic": int(Magic), "const_version": int(Version),
		"s_addr": 0, "s_data": 4, "s_va": 6, "s_weight": 8, "s_pc": 12, "s_ir": 14,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: C %d, Go %d", k, got[k], v)
		}
	}
	// The flag bits must match too (api mirrors the header).
	hdr, err := os.ReadFile(header)
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]uint32{
		"PNL_PA_MASK": api.PnlPAMask, "PNL_WR": api.PnlWR, "PNL_BYTE": api.PnlByte,
		"PNL_IFETCH": api.PnlIFetch, "PNL_WAIT": api.PnlWait, "PNL_MMU": api.PnlMMU,
		"PNL_M22": api.PnlM22, "PNL_DSPACE": api.PnlDSpace, "PNL_IOPAGE": api.PnlIOPage,
	}
	for name, v := range flags {
		m := regexp.MustCompile(`#define\s+` + name + `\s+(0x[0-9A-Fa-f]+u|\(1u << (\d+)\))`).FindStringSubmatch(string(hdr))
		if m == nil {
			t.Errorf("%s not found in header", name)
			continue
		}
		var c uint64
		if m[2] != "" {
			sh, _ := strconv.Atoi(m[2])
			c = 1 << sh
		} else {
			c, _ = strconv.ParseUint(strings.TrimSuffix(m[1][2:], "u"), 16, 32)
		}
		if uint32(c) != v {
			t.Errorf("%s: C %#x, Go %#x", name, c, v)
		}
	}
}
