package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdp11emu/server/internal/api"
	"pdp11emu/server/internal/panel/paneltest"
)

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.json")
	e := newEnv(t, withOptions(Options{StatePath: path, FrameHz: 100, Autostart: true}))
	for _, a := range []string{"sr 17654321", "halt", "sbus", "addr 6", "data 7", "key lock"} {
		e.do(a)
	}
	e.input(api.PanelInput{Kind: "momentary", Name: swLampTest, Active: true}) // not persisted
	want := e.p.Controls()
	e.stop() // Run flushes on exit

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got persistedControls
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	exp := persistedControls{Version: 1, SR: 0o17654321, EnableHalt: "halt", SinstSbus: "bus", AddrKnob: 6, DataKnob: 7, Key: keyLock}
	if got != exp {
		t.Fatalf("panel.json = %+v, want %+v", got, exp)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}

	m := paneltest.NewMachine()
	defer m.Close()
	p, err := New(m, nil, paneltest.DefaultSystems(), Options{StatePath: path, Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	c := p.Controls()
	want.LampTest = false
	if c.SR != want.SR || c.EnableHalt != want.EnableHalt || c.SinstSbus != want.SinstSbus ||
		c.AddrKnob != want.AddrKnob || c.DataKnob != want.DataKnob || c.Key != want.Key || c.LampTest {
		t.Fatalf("restored %+v, want %+v", c, want)
	}
	// New applies the remembered switch register (programs see bits 15:0).
	if sr, n := m.SR(); sr != 0o54321 || n != 1 {
		t.Fatalf("SetSwitchRegister(%o) called %d times", sr, n)
	}
}

func TestPersistenceIsDebouncedButNotLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.json")
	e := newEnv(t, withOptions(Options{StatePath: path, FrameHz: 100}))
	for i := 0; i < 200; i++ {
		e.input(api.PanelInput{Kind: "sr", Int: i})
	}
	// Written once the paddle storm is over, without stopping the panel.
	ok := paneltest.WaitFor(3*time.Second, func() bool {
		c, err := loadControls(path, quietLogger())
		return err == nil && c.sr == 199
	})
	if !ok {
		t.Fatal("panel.json not written after the changes")
	}
}

func TestLoadControls(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	def := defaultControls()
	tests := []struct {
		name string
		path string
		want controls
		bad  bool // the file is set aside
	}{
		{"missing file: first-start defaults", filepath.Join(dir, "none.json"), def, false},
		{"no persistence", "", def, false},
		{"damaged JSON", write("bad.json", `{"sr": 12`), def, true},
		{"invalid fields fall back one by one", write("fields.json",
			`{"version":1,"sr":4194304,"enableHalt":"halt","sinstSbus":"sideways","addrKnob":9,"dataKnob":3,"key":"open"}`),
			controls{sr: 0, halt: true, sbus: false, addrKnob: 0, dataKnob: 3, key: keyPower}, false},
		{"valid", write("ok.json", `{"version":1,"sr":4194303,"enableHalt":"enable","sinstSbus":"bus","addrKnob":7,"dataKnob":0,"key":"off"}`),
			controls{sr: srMask, sbus: true, addrKnob: 7, dataKnob: 0, key: keyOff}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadControls(tt.path, quietLogger())
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if _, err := os.Stat(tt.path + ".bad"); (err == nil) != tt.bad {
				t.Fatalf(".bad file exists = %v, want %v", err == nil, tt.bad)
			}
		})
	}
	t.Run("unreadable state is an error", func(t *testing.T) {
		if _, err := loadControls(dir, quietLogger()); err == nil { // a directory
			t.Fatal("no error")
		}
	})
}

func TestSaveFailureIsLoggedNotFatal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "panel.json") // the directory does not exist
	var logged strings.Builder
	e := newEnv(t, withOptions(Options{StatePath: path, FrameHz: 100, Logger: newTestLogger(&logged)}))
	e.do("sr 7")
	e.stop()
	if !strings.Contains(logged.String(), "cannot save the panel state") {
		t.Fatalf("log: %s", logged.String())
	}
	if c := e.p.Controls(); c.SR != 7 {
		t.Fatalf("SR = %o", c.SR)
	}
}

func TestSwitchRegisterIsLive(t *testing.T) {
	e := runningEnv(t)
	e.do("sr 17777777")
	if !paneltest.WaitFor(time.Second, func() bool { sr, _ := e.m.SR(); return sr == 0o177777 }) {
		sr, _ := e.m.SR()
		t.Fatalf("programs see %o, want 177777 (bits 21:16 are not program visible)", sr)
	}
	if h := e.input(api.PanelInput{Kind: "sr", Int: srMask + 1}); h == "" {
		t.Fatal("out-of-range SR accepted")
	}
	if h := e.input(api.PanelInput{Kind: "sr", Int: -1}); h == "" {
		t.Fatal("negative SR accepted")
	}
	if c := e.p.Controls(); c.SR != srMask {
		t.Fatalf("SR = %o", c.SR)
	}
}

func TestInvalidInputs(t *testing.T) {
	e := haltedEnv(t)
	for _, in := range []api.PanelInput{
		{Kind: "nonsense"},
		{Kind: "latch", Name: "ENABLE_HALT", Value: "maybe"},
		{Kind: "latch", Name: "SOMETHING", Value: "halt"},
		{Kind: "knob", Name: "addr", Int: 8},
		{Kind: "knob", Name: "addr", Int: -1},
		{Kind: "knob", Name: "volume", Int: 1},
		{Kind: "key", Value: "on"},
		{Kind: "momentary", Name: "RESET", Active: true},
	} {
		if h := e.input(in); h == "" {
			t.Errorf("%+v accepted", in)
		}
	}
	e.idle()
	if ops := e.ops(); len(ops) != 0 {
		t.Fatalf("ops %q", ops)
	}
}
