package simh

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"pdp11emu/server/internal/api"
)

func TestParseRun(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		want     api.StopInfo
		stopLine bool
		errs     []string
	}{
		{"halt instruction",
			[]string{"%SIM-INFO: Running with Buffered Console", "", "HALT instruction, PC: 001006 (HALT)"},
			api.StopInfo{Reason: "HALT instruction", PC: 0o1006, Insn: "HALT"}, true, nil},
		{"front panel halt",
			[]string{"", "Front panel HALT, PC: 001062 (SOB R3,1062)"},
			api.StopInfo{Reason: "Front panel HALT", PC: 0o1062, Insn: "SOB R3,1062", Requested: true}, true, nil},
		{"insn with parentheses",
			[]string{"", "Step expired, PC: 001004 (MOV (R1)+,-(R2))"},
			api.StopInfo{Reason: "Step expired", PC: 0o1004, Insn: "MOV (R1)+,-(R2)"}, true, nil},
		{"no instruction",
			[]string{"", "Breakpoint, PC: 000000"},
			api.StopInfo{Reason: "Breakpoint", PC: 0}, true, nil},
		{"reason containing a comma",
			[]string{"", "Trap stack push abort, PC: 000004 (HALT)"},
			api.StopInfo{Reason: "Trap stack push abort", PC: 4, Insn: "HALT"}, true, nil},
		{"last stop line wins, reset noise ignored",
			[]string{"Resetting all devices...  This may not have been your intention.",
				"The GO and CONTINUE commands do not reset devices.", "",
				"HALT instruction, PC: 001006 (HALT)", "late output, PC: 000777 (ignored?)", "", "Unit not attached, PC: 001006 (HALT)"},
			api.StopInfo{Reason: "Unit not attached", PC: 0o1006, Insn: "HALT"}, true, nil},
		{"failed boot command: no stop line",
			[]string{"%SIM-ERROR: Non-existent device: XX0"},
			api.StopInfo{}, false, []string{"%SIM-ERROR: Non-existent device: XX0"}},
		{"expect stop without output",
			[]string{"%SIM-INFO: Running with Buffered Console", ""},
			api.StopInfo{}, false, nil},
		{"expect stop with action output",
			[]string{"%SIM-INFO: Running with Buffered Console", "matched-action-output"},
			api.StopInfo{}, false, []string{"matched-action-output"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := parseRun(tc.lines)
			if r.stop != tc.want || r.stopLine != tc.stopLine || !reflect.DeepEqual(r.errs, tc.errs) {
				t.Fatalf("got %+v stopLine=%v errs=%q, want %+v %v %q", r.stop, r.stopLine, r.errs, tc.want, tc.stopLine, tc.errs)
			}
		})
	}
}

func TestParseExamine(t *testing.T) {
	tests := []struct {
		lines   []string
		want    uint16
		address bool // ErrAddress
		err     string
	}{
		{[]string{"1000:\t000123"}, 0o123, false, ""},
		{[]string{"R00:\t177777"}, 0o177777, false, ""},
		{[]string{"%SIM-INFO: something", "KSP:\t000004"}, 4, false, ""},
		{[]string{"17777570:\t000000"}, 0, false, ""},
		{[]string{"Address space exceeded"}, 0, true, "Address space exceeded"},
		{[]string{"Relocation error"}, 0, true, "Relocation error"},
		{[]string{"Invalid argument"}, 0, true, "Invalid argument"},
		{[]string{"Unknown command"}, 0, false, "Unknown command"},
		{[]string{"PSW:\t000000\tCM=K PM=K"}, 0, false, "CM=K"},
		{nil, 0, false, "no reply"},
		{[]string{"1000:\t000123", "1002:\t000000"}, 0, false, "1002"},
	}
	for _, tc := range tests {
		v, err := parseExamine("examine x", tc.lines)
		switch {
		case tc.err == "":
			if err != nil || v != tc.want {
				t.Errorf("%q: got %o, %v; want %o", tc.lines, v, err, tc.want)
			}
		case err == nil:
			t.Errorf("%q: no error", tc.lines)
		case errors.Is(err, api.ErrAddress) != tc.address || !strings.Contains(err.Error(), tc.err):
			t.Errorf("%q: error %v (address=%v), want %q address=%v", tc.lines, err, errors.Is(err, api.ErrAddress), tc.err, tc.address)
		}
	}
	if err := checkEmpty("deposit 17777000 1", []string{"Address space exceeded"}); !errors.Is(err, api.ErrAddress) {
		t.Errorf("deposit error %v", err)
	}
	if err := checkEmpty("set throttle 1M", []string{"Idling disabled"}); err != nil {
		t.Errorf("informational reply treated as error: %v", err)
	}
	if err := checkEmpty("set cpu idle=1", []string{"%SIM-ERROR: Invalid Stability value: 1.  Valid values range from 2 to 600."}); err == nil ||
		strings.Contains(err.Error(), "%SIM-ERROR") {
		t.Errorf("set error %v", err)
	}
}

func TestParseDiagnostics(t *testing.T) {
	const dir = "/data/run"
	// file builds command-file contents with the given (1-based) lines.
	file := func(at map[int]string) []string {
		n := 0
		for k := range at {
			n = max(n, k)
		}
		out := make([]string, n)
		for k, v := range at {
			out[k-1] = v
		}
		return out
	}
	tests := []struct {
		name string
		out  []string
		file []string
		want []diagnostic
	}{
		{
			name: "clean start-up (review exp1_stock)",
			out: []string{"", "PDP-11 simulator Open SIMH V4.1-0 Current        git commit id: 87eb7d5e", "Disabling XQ",
				dir + "/run.ini-6> set console -u telnet=127.0.0.1:47100;notelnet", "%SIM-INFO: Listening on port 127.0.0.1:47100"},
		},
		{
			name: "configuration errors (review exp1d_cfgerr)",
			out: []string{"", "PDP-11 simulator Open SIMH V4.1-0", "Disabling XQ",
				dir + "/run.ini-6> set console -u telnet=127.0.0.1:47800;notelnet", "%SIM-INFO: Listening on port 127.0.0.1:47800",
				dir + "/run.ini-10> attach rq0 /x/missing.dsk", "%SIM-INFO: RQ0: Creating new file: /x/missing.dsk",
				dir + "/run.ini-11> attach -e rq1 /x/missing2.dsk", "%SIM-ERROR: RQ1: Cannot open '/x/missing2.dsk' - No such file or directory",
				dir + "/run.ini-12> set bogus on", "%SIM-ERROR: Non-existent device: BOGUS",
				dir + "/run.ini-14> attach -u -m dz Line=9,127.0.0.1:47801;notelnet", "%SIM-ERROR: Invalid Line Specifier: 9",
				"reached-end-of-config"},
			file: file(map[int]string{14: "attach -u -m dz Line=9,127.0.0.1:47801;notelnet", 15: "echo reached-end-of-config"}),
			want: []diagnostic{
				{11, "attach -e rq1 /x/missing2.dsk", "RQ1: Cannot open '/x/missing2.dsk' - No such file or directory"},
				{12, "set bogus on", "Non-existent device: BOGUS"},
				{14, "attach -u -m dz Line=9,127.0.0.1:47801;notelnet", "Invalid Line Specifier: 9"},
			},
		},
		{
			name: "busy console port (review exp1f_portbusy)",
			out: []string{"", "PDP-11 simulator", "Disabling XQ", "Sockets: bind error 98 - Address already in use",
				dir + "/run.ini-6> set console -u telnet=127.0.0.1:47880;notelnet", "%SIM-ERROR: Can't open network port: 127.0.0.1:47880"},
			want: []diagnostic{{6, "set console -u telnet=127.0.0.1:47880;notelnet", "Can't open network port: 127.0.0.1:47880"}},
		},
		{
			name: "modem control notice is informational",
			out:  []string{dir + "/run.ini-20> attach -u -m dz :4000", "%SIM-INFO: Listening on port :4000", "Modem control activated"},
		},
		{
			name: "echo output is not attributed to an earlier line",
			out: []string{dir + "/run.ini-9> attach -e rl0 /d/rsts.dsk", "%SIM-INFO: RL0: '/d/rsts.dsk' Contains a RSTS File system",
				"Booting RSTS/E 7 (date %DATE%)", "Hello"},
			file: file(map[int]string{9: "attach -e rl0 /d/rsts.dsk", 10: "echo Booting RSTS/E 7 (date %DATE_YYYY%)", 11: "echo Hello"}),
		},
		{
			name: "plain message after an echoed line is an error",
			out:  []string{dir + "/run.ini-9> attach -e rk0 /d/a.dsk", "%SIM-INFO: RK0: ok", "something odd happened"},
			want: []diagnostic{{9, "attach -e rk0 /d/a.dsk", "something odd happened"}},
		},
		{
			name: "echoes of another file are not ours",
			out:  []string{dir + "/dialog.do-3> expect", "Too few arguments"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDiagnostics(tc.out, "run.ini", tc.file)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestDialogErrors(t *testing.T) {
	if e := dialogErrors(nil, []string{`expect "@" send "x"; continue`}); len(e) != 0 {
		t.Fatalf("clean dialogue reported %q", e)
	}
	e := dialogErrors([]string{"/data/run/dialog.do-4> expect", "Too few arguments"}, []string{`expect "a" send "b"; continue`, "expect"})
	if len(e) != 1 || !strings.Contains(e[0], "dialog line 2") || !strings.Contains(e[0], "Too few arguments") {
		t.Fatalf("got %q", e)
	}
	if e := dialogErrors([]string{"File open error"}, nil); len(e) != 1 || e[0] != "File open error" {
		t.Fatalf("got %q", e)
	}
}
