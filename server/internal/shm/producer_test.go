package shm

import (
	"math"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"unsafe"

	"pdp11emu/server/internal/api"
)

// producer is a Go stand-in for the simulator side of the export: it writes
// the exact C layout of pdp11_panel_shm.h with the same atomics protocol.
type producer struct {
	mem []byte
	wr  uint32 // samples written (mirrors ring_wr)
}

// alignedBuffer returns a zeroed, 8-byte aligned buffer of FileSize bytes
// on the Go heap, so the race detector observes every access.
func alignedBuffer() []byte {
	words := make([]uint64, FileSize/8)
	return unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), FileSize)
}

// mappedFile creates path with FileSize bytes and maps it shared, as the
// simulator's ATTACH PANEL does.
func mappedFile(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(FileSize); err != nil {
		t.Fatal(err)
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, FileSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Munmap(mem) })
	return mem
}

func newProducer(mem []byte, ringWr uint32) *producer {
	p := &producer{mem: mem, wr: ringWr}
	p.store(OffMagic, Magic)
	p.store(OffVersion, Version)
	p.store(OffSize, FileSize)
	p.store(OffRingSize, RingSize)
	p.store(OffRingWr, ringWr)
	return p
}

func (p *producer) word(off int) *uint32    { return (*uint32)(unsafe.Pointer(&p.mem[off])) }
func (p *producer) store(off int, v uint32) { atomic.StoreUint32(p.word(off), v) }
func (p *producer) load(off int) uint32     { return atomic.LoadUint32(p.word(off)) }
func (p *producer) store64(off int, v uint64) {
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&p.mem[off])), v)
}

// expected is the sample the producer writes as sample number k. Every
// field depends on k so a torn or misplaced entry is detectable.
func expected(k uint32) (addr uint32, data, va uint16, weight uint32, pc, ir uint16) {
	return k, uint16(k * 7), uint16(k >> 3), k | 1, uint16(k * 3), uint16(k>>16) ^ 0x5555
}

// write appends n samples and publishes each with a release store of
// ring_wr, like panel_sample().
func (p *producer) write(n int) {
	for i := 0; i < n; i++ {
		addr, data, va, weight, pc, ir := expected(p.wr)
		off := OffRing + int(p.wr%RingSize)*SampleSize
		p.store(off, addr)
		p.store(off+4, uint32(data)|uint32(va)<<16)
		p.store(off+8, weight)
		p.store(off+12, uint32(pc)|uint32(ir)<<16)
		p.wr++
		p.store(OffRingWr, p.wr)
	}
}

// block writes the register block for generation g under the seqlock.
// Every field is derived from g so a torn copy is detectable.
func (p *producer) block(g uint32) {
	seq := p.load(OffSeq)
	p.store(OffSeq, seq+1) // odd: write in progress
	for i := 0; i < 8; i++ {
		p.store(OffR+4*i, (g+uint32(i))&0xffff)
	}
	p.store(OffPSW, g&0xffff)
	p.store(OffDR, ^g&0xffff)
	p.store(OffMMR0, (g*3)&0xffff)
	p.store(OffMMR3, (g*5)&0xffff)
	p.store(OffLastPA, g*7)
	p.store(OffLastData, (g*11)&0xffff)
	p.store64(OffSimTime, math.Float64bits(float64(g)))
	p.store(OffSampleInterval, g+13)
	p.store(OffSamplesPerSec, g+17)
	p.store(OffPCPA, g*19)
	p.store64(OffInstrCount, uint64(g)*1000)
	p.store(OffSeq, seq+2) // even: stable
}

// serviceHalt honours a pending halt request (halt_ack = halt_req) and
// reports whether there was one, like the simulator's sampler.
func (p *producer) serviceHalt() bool {
	req := p.load(OffHaltReq)
	if req == p.load(OffHaltAck) {
		return false
	}
	p.store(OffHaltAck, req)
	return true
}

// checkBlock verifies that a snapshot is one consistent generation.
func checkBlock(t *testing.T, s api.PanelSnapshot) uint32 {
	t.Helper()
	g := uint32(s.SimTime)
	ok := float64(g) == s.SimTime
	for i := 0; i < 8; i++ {
		ok = ok && uint32(s.R[i]) == (g+uint32(i))&0xffff
	}
	ok = ok && uint32(s.PSW) == g&0xffff && uint32(s.DR) == ^g&0xffff &&
		uint32(s.MMR0) == (g*3)&0xffff && uint32(s.MMR3) == (g*5)&0xffff &&
		s.LastPA == g*7 && uint32(s.LastData) == (g*11)&0xffff &&
		s.SampleInterval == g+13 && s.SamplesPerSec == g+17 &&
		s.PCPA == g*19 && s.InstrCount == uint64(g)*1000
	if !ok {
		t.Fatalf("torn snapshot: %+v", s)
	}
	return g
}
