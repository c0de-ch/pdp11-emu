package simh

import (
	"fmt"
	"strings"

	"pdp11emu/server/internal/api"
)

// Physical address space of the 11/70 and the I/O page.
const (
	maxPhys    = 1<<22 - 1 // 17777777
	ioPage18   = 0760000
	ioPage16   = 0160000
	mmr0Enable = 1   // MMR0<0>: relocation enabled
	mmr3M22    = 020 // MMR3<4>: 22-bit mapping
	parMask    = 0177777
	blockMask  = 017777 // VA<12:0>, displacement within a page
)

// registers are the CPU registers a console EXAMINE/DEPOSIT may name
// (api.Location.Register).
var registers = map[string]bool{
	"R00": true, "R01": true, "R02": true, "R03": true, "R04": true, "R05": true,
	"R10": true, "R11": true, "R12": true, "R13": true, "R14": true, "R15": true,
	"KSP": true, "SSP": true, "USP": true, "PC": true,
}

// locationArg renders loc as SimH EXAMINE/DEPOSIT arguments built only
// from validated values ("R00", "17777570", "-v -k -t 1000").
func locationArg(loc api.Location) (string, error) {
	if loc.Register != "" {
		r := strings.ToUpper(loc.Register)
		if !registers[r] {
			return "", fmt.Errorf("%w: unknown register %q", api.ErrAddress, loc.Register)
		}
		return r, nil
	}
	if !loc.Virtual {
		if loc.Addr > maxPhys {
			return "", fmt.Errorf("%w: physical address %o exceeds 22 bits", api.ErrAddress, loc.Addr)
		}
		return fmt.Sprintf("%o", loc.Addr), nil
	}
	if loc.Addr > 0177777 {
		return "", fmt.Errorf("%w: virtual address %o exceeds 16 bits", api.ErrAddress, loc.Addr)
	}
	sw, err := modeSwitch(loc.Mode)
	if err != nil {
		return "", err
	}
	if loc.DSpace {
		sw += " -t" // SimH's D-space switch (-d means decimal)
	}
	return fmt.Sprintf("-v %s %o", sw, loc.Addr), nil
}

func modeSwitch(m api.CPUMode) (string, error) {
	switch m {
	case api.ModeKernel:
		return "-k", nil
	case api.ModeSuper:
		return "-s", nil
	case api.ModeUser:
		return "-u", nil
	}
	return "", fmt.Errorf("%w: invalid CPU mode %d", api.ErrAddress, m)
}

// parName returns the SimH register holding the page address register for
// a mode, address space and page (e.g. "KDPAR3").
func parName(m api.CPUMode, dspace bool, page int) string {
	mode := map[api.CPUMode]string{api.ModeKernel: "K", api.ModeSuper: "S", api.ModeUser: "U"}[m]
	space := "I"
	if dspace {
		space = "D"
	}
	return fmt.Sprintf("%s%sPAR%d", mode, space, page)
}

// dspaceEnabled reports MMR3's D-space enable for a mode (kernel bit 2,
// supervisor bit 1, user bit 0). Without it D references use I-space
// registers, exactly as SimH's relocC does.
func dspaceEnabled(m api.CPUMode, mmr3 uint16) bool {
	switch m {
	case api.ModeKernel:
		return mmr3&04 != 0
	case api.ModeSuper:
		return mmr3&02 != 0
	case api.ModeUser:
		return mmr3&01 != 0
	}
	return false
}

// translate maps a 16-bit virtual address like the KB11-C (SimH relocC):
// relocation off: 16-bit, 160000-177777 is the I/O page; on: PA =
// (PAR<<6) + VA<12:0>, truncated to 18 bits unless MMR3<4> selects 22-bit
// mapping, with the 18-bit I/O page 760000-777777 at 17760000. readPAR is
// called only when relocation is on.
func translate(va uint16, mode api.CPUMode, dspace bool, mmr0, mmr3 uint16, readPAR func(name string) (uint16, error)) (uint32, error) {
	if mmr0&mmr0Enable == 0 {
		pa := uint32(va)
		if pa >= ioPage16 {
			pa |= 017600000
		}
		return pa, nil
	}
	if _, err := modeSwitch(mode); err != nil {
		return 0, err
	}
	par, err := readPAR(parName(mode, dspace && dspaceEnabled(mode, mmr3), int(va>>13)))
	if err != nil {
		return 0, err
	}
	pa := (uint32(va)&blockMask + uint32(par&parMask)<<6) & maxPhys
	if mmr3&mmr3M22 == 0 {
		pa &= 0777777
		if pa >= ioPage18 {
			pa |= 017000000
		}
	}
	return pa, nil
}
