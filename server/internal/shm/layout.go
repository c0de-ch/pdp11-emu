// Package shm maps the front-panel export file written by the patched
// simulator (simh/panel/pdp11_panel_shm.h is canonical; offsets mirror it).
package shm

// Layout of PANEL_SHM version 3 (little-endian, byte offsets).
const (
	Magic    uint32 = 0x50313150 // 'P','1','1','P'
	Version  uint32 = 3
	RingSize        = 16384
	FileSize        = 256 + 16*RingSize // 262400

	OffMagic          = 0
	OffVersion        = 4
	OffSize           = 8
	OffRingSize       = 12
	OffRingWr         = 16
	OffState          = 20
	OffSeq            = 24
	OffStopCount      = 28
	OffR              = 32 // 8 x uint32
	OffPSW            = 64
	OffDR             = 68
	OffMMR0           = 72
	OffMMR3           = 76
	OffLastPA         = 80
	OffLastData       = 84
	OffSimTime        = 88 // float64
	OffSampleInterval = 96
	OffSamplesPerSec  = 100
	OffSR             = 104 // bridge -> sim
	OffSRValid        = 108 // bridge -> sim
	OffHaltReq        = 112 // bridge -> sim
	OffTargetRate     = 116 // bridge -> sim
	OffTrapCount      = 120
	OffHaltAck        = 124
	OffPCPA           = 128
	OffInstrCount     = 136 // uint64
	OffDROn           = 144 // 16 x uint32, with every sample
	OffWeightSum      = 208 // with every sample
	OffRing           = 256
	SampleSize        = 16 // addr u32, data u16, va u16, weight u32 (wall µs), pc u16, ir u16
)
