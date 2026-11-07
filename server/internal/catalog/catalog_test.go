package catalog

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

// testSystem returns a valid system.json as a map, for a test to change.
func testSystem() map[string]any {
	return map[string]any{
		"id": "sys", "name": "A system", "short": "S", "category": "unix", "default": true, "order": 1,
		"description": "d", "license": map[string]any{"name": "L", "class": "redistributable", "summary": "s"},
		"noLogin": true, "shutdown": "s", "notes": "## Log in\n## Things to try\n## Shut down\n",
		"cpu": map[string]any{"memory": "1M"}, "speed": "fast", "boot": "boot rk0", "readyPattern": "login: ",
		"console": map[string]any{"backspace": "del", "interrupt": "ctrl-c"},
		"artifacts": []map[string]any{{"url": "https://example.invalid/x.zip", "sha256": strings.Repeat("a", 64), "size": 1, "format": "zip",
			"files": []map[string]any{{"path": "disk.dsk", "role": "DISK0", "sha256": strings.Repeat("b", 64), "size": 1}}}},
	}
}

func validate(t *testing.T, sys map[string]any) []error {
	t.Helper()
	raw, err := json.Marshal(sys)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := Load(fstest.MapFS{
		"sys/system.json": {Data: raw},
		"sys/boot.ini":    {Data: []byte("attach -e rk0 ${DISK0}\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cat.Validate()
}

type step = map[string]any

func TestShutdownScriptValidation(t *testing.T) {
	guard := step{"ifLineEnds": []string{"\nlogin: *", "\n# "}, "else": "not at a prompt"}
	last := step{"expect": "# ", "send": "halt\r"}
	for _, tt := range []struct {
		name   string
		script []step
		want   string // empty: valid
	}{
		{name: "a typical script", script: []step{guard,
			{"ifLineEnds": []string{"\nlogin: *"}, "send": "root\r"},
			{"expectAny": []string{"# ", "Password:"}, "timeoutSec": 15, "else": "no prompt"},
			{"ifLineEnds": []string{"# "}, "else": "a password"},
			last}},
		{name: "ending at a safe point", script: []step{guard, {"expect": "Option: ", "timeoutSec": 120}}},
		{name: "ending with expectAny", script: []step{guard, {"expectAny": []string{"a", "b"}}}},
		{name: "the guard is the first thing", script: []step{{"send": "root\r"}, guard, last},
			want: "step 0: the first step must check the console"},
		{name: "a first step that only waits", script: []step{{"expect": "login: ", "send": "root\r"}, last},
			want: "step 0: the first step must check the console"},
		{name: "a first step that does not say what to do", script: []step{{"ifLineEnds": []string{"\n# "}}, last},
			want: "step 0: the first step must check the console"},
		{name: "the last step is a condition", script: []step{guard, {"ifLineEnds": []string{"\n# "}, "send": "halt\r"}},
			want: "the last step tells how the shutdown ends"},
		{name: "the last step only fails", script: []step{guard},
			want: "the last step tells how the shutdown ends"},
		{name: "an empty step", script: []step{guard, {}, last}, want: "step 1 is empty"},
		{name: "a step with only a condition", script: []step{guard, {"ifLineEnds": []string{"# "}}, last}, want: "step 1 is empty"},
		{name: "expect and expectAny", script: []step{guard, {"expect": "a", "expectAny": []string{"b", "c"}}, last},
			want: "expect and expectAny exclude each other"},
		{name: "one text for expectAny", script: []step{guard, {"expectAny": []string{"a"}}, last}, want: "at least two texts"},
		{name: "an empty text for expectAny", script: []step{guard, {"expectAny": []string{"a", ""}}, last}, want: "an empty text"},
		{name: "else without a condition or a wait", script: []step{guard, {"send": "x\r", "else": "no"}, last},
			want: "else needs ifLineEnds"},
		{name: "a carriage return in a text", script: []step{{"ifLineEnds": []string{"\r\n# "}, "else": "no"}, last},
			want: "a line end is written"},
		{name: "a fill character in a text", script: []step{guard, {"ifLineEnds": []string{">\x00"}, "send": "x"}, last},
			want: "control characters never match"},
		{name: "an escape in a text", script: []step{guard, {"ifLineEnds": []string{"\x1b[H# "}, "send": "x"}, last},
			want: "control characters never match"},
		{name: "no text at all", script: []step{guard, {"ifLineEnds": []string{""}, "send": "x"}, last}, want: "needs some text"},
		{name: "only line ends and stars", script: []step{guard, {"ifLineEnds": []string{"\n*"}, "send": "x"}, last}, want: "needs some text"},
		{name: "a negative timeout", script: []step{guard, {"expect": "a", "timeoutSec": -1}, last}, want: "is negative"},
		{name: "Ctrl+C", script: []step{guard, {"send": "\x03"}, last}, want: "holds an interrupt"},
		{name: "DEL, even at a known prompt", script: []step{guard, {"ifLineEnds": []string{"# "}, "send": "\x7f"}, last}, want: "holds an interrupt"},
		{name: "Ctrl+D blindly", script: []step{guard, {"send": "\x04"}, last}, want: "needs ifLineEnds"},
		{name: "Ctrl+D at a known prompt", script: []step{guard, {"ifLineEnds": []string{"\n$ "}, "send": "\x04"}, last}},
		{name: "a kill at a known prompt", script: []step{guard, {"ifLineEnds": []string{"\nlogin: *"}, "send": "\x15root\r"}, last}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sys := testSystem()
			sys["shutdownScript"] = tt.script
			errs := validate(t, sys)
			if tt.want == "" {
				if len(errs) != 0 {
					t.Fatalf("errors: %v", errs)
				}
				return
			}
			for _, err := range errs {
				if strings.Contains(err.Error(), tt.want) {
					return
				}
			}
			t.Fatalf("errors %v, want %q", errs, tt.want)
		})
	}
	t.Run("no script at all is fine", func(t *testing.T) {
		if errs := validate(t, testSystem()); len(errs) != 0 {
			t.Fatalf("errors: %v", errs)
		}
	})
}

// The session of the boot matrix types what it was told: the fields that look
// at the console belong to shutdown scripts.
func TestSessionStepsStayPlain(t *testing.T) {
	for _, st := range []step{
		{"ifLineEnds": []string{"# "}, "send": "x\r"},
		{"expectAny": []string{"a", "b"}},
		{"expect": "a", "else": "no"},
	} {
		sys := testSystem()
		sys["tests"] = map[string]any{"session": []step{st}}
		errs := validate(t, sys)
		found := false
		for _, err := range errs {
			found = found || strings.Contains(err.Error(), "belong in a shutdownScript")
		}
		if !found {
			t.Errorf("session step %v: errors %v", st, errs)
		}
	}
}
