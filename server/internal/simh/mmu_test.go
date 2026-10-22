package simh

import (
	"errors"
	"fmt"
	"testing"

	"pdp11emu/server/internal/api"
)

func TestLocationArg(t *testing.T) {
	tests := []struct {
		loc     api.Location
		want    string
		address bool // error wraps ErrAddress
	}{
		{api.Location{Register: "r00"}, "R00", false},
		{api.Location{Register: "PC"}, "PC", false},
		{api.Location{Register: "R15"}, "R15", false},
		{api.Location{Register: "SSP"}, "SSP", false},
		{api.Location{Register: "R06"}, "", true},
		{api.Location{Register: "PSW; quit"}, "", true},
		{api.Location{Addr: 0o17777570}, "17777570", false},
		{api.Location{Addr: 0}, "0", false},
		{api.Location{Addr: 0o20000000}, "", true},
		{api.Location{Addr: 0o1000, Virtual: true, Mode: api.ModeKernel}, "-v -k 1000", false},
		{api.Location{Addr: 0o1000, Virtual: true, Mode: api.ModeSuper, DSpace: true}, "-v -s -t 1000", false},
		{api.Location{Addr: 0o177776, Virtual: true, Mode: api.ModeUser, DSpace: true}, "-v -u -t 177776", false},
		{api.Location{Addr: 0o200000, Virtual: true}, "", true},
		{api.Location{Addr: 0o1000, Virtual: true, Mode: 2}, "", true},
	}
	for _, tc := range tests {
		got, err := locationArg(tc.loc)
		if tc.address {
			if !errors.Is(err, api.ErrAddress) {
				t.Errorf("%+v: got %q, %v; want ErrAddress", tc.loc, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%+v: got %q, %v; want %q", tc.loc, got, err, tc.want)
		}
	}
}

func TestTranslate(t *testing.T) {
	pars := map[string]uint16{
		"KIPAR0": 0o000000, "KIPAR1": 0o000200, "KIPAR7": 0o177600,
		"KDPAR1": 0o010000, // 22-bit: 01000000
		"KDPAR7": 0o007600, // 18-bit I/O page
		"SIPAR3": 0o001234, "SDPAR3": 0o002000,
		"UIPAR0": 0o054321, "UDPAR0": 0o000100,
	}
	tests := []struct {
		name       string
		va         uint16
		mode       api.CPUMode
		dspace     bool
		mmr0, mmr3 uint16
		want       uint32
		read       string // PAR expected to be read
	}{
		{"mmu off", 0o1000, api.ModeKernel, false, 0, 0, 0o1000, ""},
		{"mmu off, I/O page", 0o177570, api.ModeUser, true, 0, 0, 0o17777570, ""},
		{"mmu off, top of low memory", 0o157776, api.ModeKernel, false, 0, 0, 0o157776, ""},
		{"kernel I page 1, 18-bit", 0o20123, api.ModeKernel, false, 1, 0, 0o20000 + 0o123, "KIPAR1"},
		{"kernel D without D-space enable uses I-space", 0o20004, api.ModeKernel, true, 1, 0o020, 0o20004, "KIPAR1"},
		{"kernel D with D-space enable, 22-bit", 0o20004, api.ModeKernel, true, 1, 0o024, 0o1000004, "KDPAR1"},
		{"kernel D, 18-bit truncates", 0o20004, api.ModeKernel, true, 1, 0o004, 0o000004, "KDPAR1"},
		{"18-bit I/O page maps to 17760000+", 0o177570, api.ModeKernel, true, 1, 0o004, 0o17777570, "KDPAR7"},
		{"22-bit I/O page via PAR 177600", 0o160000, api.ModeKernel, false, 1, 0o020, 0o17760000, "KIPAR7"},
		{"supervisor I", 0o60010, api.ModeSuper, false, 1, 0o022, 0o123400 + 0o10, "SIPAR3"},
		{"supervisor D enabled by MMR3<1>", 0o60010, api.ModeSuper, true, 1, 0o022, 0o200000 + 0o10, "SDPAR3"},
		{"user D enabled by MMR3<0>", 0o00100, api.ModeUser, true, 1, 0o021, 0o10000 + 0o100, "UDPAR0"},
		{"user I, 22-bit", 0o00100, api.ModeUser, false, 1, 0o021, 0o5432100 + 0o100, "UIPAR0"},
		{"user I, 18-bit", 0o00100, api.ModeUser, false, 1, 0o001, (0o5432100 + 0o100) & 0o777777, "UIPAR0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var read []string
			got, err := translate(tc.va, tc.mode, tc.dspace, tc.mmr0, tc.mmr3, func(n string) (uint16, error) {
				read = append(read, n)
				v, ok := pars[n]
				if !ok {
					return 0, fmt.Errorf("unexpected register %s", n)
				}
				return v, nil
			})
			if err != nil || got != tc.want {
				t.Fatalf("got %o, %v; want %o", got, err, tc.want)
			}
			if tc.read == "" && len(read) != 0 || tc.read != "" && (len(read) != 1 || read[0] != tc.read) {
				t.Fatalf("read %v, want %s", read, tc.read)
			}
		})
	}
	if _, err := translate(0o1000, 2, false, 1, 0, nil); !errors.Is(err, api.ErrAddress) {
		t.Fatalf("mode 2: %v", err)
	}
}
