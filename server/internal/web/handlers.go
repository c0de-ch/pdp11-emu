package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pdp11emu/server/internal/api"
)

// handleText validates and executes one JSON message of a client
// (PROTOCOL.md §1.3). Answers to invalid or ignored input go to the
// sender only, with its req.
func (c *client) handleText(data []byte) {
	var m clientMsg
	if err := json.Unmarshal(data, &m); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			c.toast("error", fmt.Sprintf("Invalid message: field %q has the wrong type.", te.Field), m.Req)
		} else {
			c.toast("error", "Invalid message: not a JSON object.", m.Req)
		}
		return
	}
	bad := func(format string, a ...any) { c.toast("error", "Invalid message: "+fmt.Sprintf(format, a...), m.Req) }
	s := c.h.s
	switch m.Type {
	case "subscribe":
		if m.Sub == nil {
			bad(`"subscribe" needs "sub": [channels].`)
			return
		}
		c.onSubscribe(m.Sub)
	case "sr":
		v, ok := intValue(m.Value)
		if !ok || v < 0 || v > 1<<22-1 {
			bad(`"sr" needs an integer "value" of 22 bits.`)
			return
		}
		c.panelInput(api.PanelInput{Kind: "sr", Int: v}, m.Req)
	case "momentary":
		switch m.Name {
		case "LOAD_ADRS", "EXAM", "DEP", "CONT", "START", "LAMP_TEST":
		default:
			bad("unknown momentary switch %q.", m.Name)
			return
		}
		if m.Active == nil {
			bad(`"momentary" needs a boolean "active".`)
			return
		}
		c.panelInput(api.PanelInput{Kind: "momentary", Name: m.Name, Active: *m.Active}, m.Req)
	case "latch":
		v, ok := stringValue(m.Value)
		switch {
		case !ok:
			bad(`"latch" needs a string "value".`)
		case m.Name == "ENABLE_HALT" && (v == "enable" || v == "halt"),
			m.Name == "SINST_SBUS" && (v == "inst" || v == "bus"):
			c.panelInput(api.PanelInput{Kind: "latch", Name: m.Name, Value: v}, m.Req)
		default:
			bad("unknown latch setting %s=%q.", m.Name, v)
		}
	case "knob":
		pos, ok := intValue(m.Pos)
		if (m.Name != "addr" && m.Name != "data") || !ok || pos < 0 || pos > 7 {
			bad(`"knob" needs "name": "addr"|"data" and "pos": 0..7.`)
			return
		}
		c.panelInput(api.PanelInput{Kind: "knob", Name: m.Name, Int: pos}, m.Req)
	case "key":
		pos, ok := stringValue(m.Pos)
		if !ok || (pos != "off" && pos != "power" && pos != "lock") {
			bad(`"key" needs "pos": "off"|"power"|"lock".`)
			return
		}
		c.panelInput(api.PanelInput{Kind: "key", Value: pos}, m.Req)
	case "boot", "shutdownOS":
		c.panelInput(api.PanelInput{Kind: m.Type}, m.Req)
	case "selectSystem":
		c.onSelect(m.ID, m.Req)
	case "install":
		c.onInstall(m.ID, m.AcceptLicense != nil && *m.AcceptLicense, m.Req)
	case "factoryReset":
		c.onFactoryReset(m.ID, m.Req)
	case "cancel":
		if m.Task != "install" && m.Task != "factoryReset" {
			bad(`"cancel" needs "task": "install"|"factoryReset".`)
			return
		}
		if _, ok := s.deps.Systems.Get(m.ID); !ok {
			bad("unknown system %q.", m.ID)
			return
		}
		s.deps.Systems.Cancel(m.ID)
	case "speed":
		v, ok := stringValue(m.Value)
		if !ok || (v != string(api.SpeedFast) && v != string(api.SpeedAuthentic)) {
			bad(`"speed" needs "value": "fast"|"authentic".`)
			return
		}
		c.onSpeed(api.Speed(v), m.Req)
	case "inputCancel":
		if st := s.deps.Machine.Console(); st != nil {
			st.CancelInput()
		}
		c.h.markState()
	case "ping":
		c.push(outMsg{kind: kindJSON, data: []byte(`{"type":"pong"}`)}, true)
	case "":
		bad(`no "type".`)
	default:
		bad("unknown type %q.", m.Type)
	}
}

// intValue accepts a JSON integer (not a string, not a fraction).
func intValue(raw json.RawMessage) (int, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	v, err := strconv.ParseInt(string(raw), 10, 32)
	if err != nil {
		return 0, false
	}
	return int(v), true
}

// stringValue accepts a JSON string.
func stringValue(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// panelInput forwards a control to the panel; an ignored input is
// explained to the sender.
func (c *client) panelInput(in api.PanelInput, req string) {
	in.Client, in.Req = c.id, req
	if hint := c.h.s.deps.Panel.Input(in); hint != "" {
		c.toast("warn", hint, req)
	}
}

func (c *client) retryPower(req string) {
	c.h.s.deps.Panel.Input(api.PanelInput{Kind: "retryPower", Req: req})
}

func (c *client) onSubscribe(names []string) {
	want, unknown := parseChannels(names)
	for ch := channel(0); ch < numChannels; ch++ {
		switch on := c.subscribed(ch); {
		case want[ch] && !on:
			c.subscribe(ch)
		case !want[ch] && on:
			c.unsubscribe(ch)
		}
	}
	if len(unknown) > 0 {
		c.toast("warn", "Unsupported channels ignored: "+strings.Join(unknown, ", "), "")
	}
}

// changeGuard: another system may be selected (and the selected one
// factory reset) only while the machine is off or failed and the console
// is not locked (PANEL_LOGIC.md §3).
func (s *Server) changeGuard() string {
	run := s.deps.Machine.State()
	switch {
	case s.deps.Panel.Controls().Key == "lock":
		return "The console is locked: turn the key to POWER or OFF first."
	case run != api.RunOff && run != api.RunError:
		return "Power the machine off first (shut down the OS, then turn the key to OFF)."
	}
	if busy := s.deps.Panel.Console().Busy; busy != "" {
		return "The machine is busy (" + busy + ")."
	}
	return ""
}

func (c *client) systemName(id, req string) (string, bool) {
	info, ok := c.h.s.deps.Systems.Get(id)
	if !ok {
		c.toast("error", fmt.Sprintf("Invalid message: unknown system %q.", id), req)
		return "", false
	}
	if info.Name == "" {
		return id, true
	}
	return info.Name, true
}

func (c *client) onSelect(id, req string) {
	s := c.h.s
	name, ok := c.systemName(id, req)
	if !ok {
		return
	}
	if hint := s.changeGuard(); hint != "" {
		c.toast("warn", hint, req)
		return
	}
	switch err := s.deps.Systems.Select(id); {
	case err == nil:
		c.retryPower(req)
	case errors.Is(err, api.ErrBusy): // a power-on started meanwhile, or a simulator is still stopping
		c.toast("warn", name+" cannot be selected while the simulator runs; try again once the machine is off.", req)
	default:
		c.toast("error", fmt.Sprintf("Cannot select %s: %v", name, err), req)
	}
}

func (c *client) onInstall(id string, accept bool, req string) {
	s := c.h.s
	name, ok := c.systemName(id, req)
	if !ok {
		return
	}
	s.goTask(func(ctx context.Context) {
		err := s.deps.Systems.Install(ctx, id, accept, req)
		switch {
		case err == nil:
			if id == s.deps.Systems.Selected() {
				c.retryPower(req)
			}
		case ctx.Err() != nil:
		case errors.Is(err, api.ErrBusy):
			c.toast("warn", name+" is already being installed.", req)
		case errors.Is(err, context.Canceled): // a user's cancel: the "cancelled" progress says so
		default:
			c.toast("error", fmt.Sprintf("Installing %s failed: %v", name, err), req)
		}
	})
}

func (c *client) onFactoryReset(id, req string) {
	s := c.h.s
	name, ok := c.systemName(id, req)
	if !ok {
		return
	}
	if id == s.deps.Systems.Selected() {
		if hint := s.changeGuard(); hint != "" {
			c.toast("warn", hint, req)
			return
		}
	}
	s.goTask(func(ctx context.Context) {
		err := s.deps.Systems.FactoryReset(ctx, id, req)
		switch {
		case err == nil:
			if id == s.deps.Systems.Selected() {
				c.retryPower(req)
			}
		case ctx.Err() != nil:
		case errors.Is(err, api.ErrBusy):
			c.toast("warn", name+" is busy (being installed or reset, or in use by the simulator).", req)
		case errors.Is(err, context.Canceled): // a user's cancel: the "cancelled" progress says so
		default:
			c.toast("error", fmt.Sprintf("Factory reset of %s failed: %v", name, err), req)
		}
	})
}

// onSpeed persists the speed of the selected system and applies it to a
// powered simulator (DESIGN.md §9).
func (c *client) onSpeed(sp api.Speed, req string) {
	s := c.h.s
	if err := s.deps.Systems.SetSpeedFor(s.deps.Systems.Selected(), sp); err != nil {
		c.toast("error", "Cannot change the speed: "+err.Error(), req)
		return
	}
	c.h.markState()
	if !s.deps.Machine.State().Powered() {
		return
	}
	s.goTask(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := s.deps.Machine.SetSpeed(ctx, sp); err != nil && !errors.Is(err, api.ErrNotPowered) && ctx.Err() == nil {
			c.toast("error", "Cannot change the speed of the running machine: "+err.Error(), req)
		}
		c.h.markState()
	})
}

// goTask runs a client-initiated background operation (install, reset,
// speed change) with the server's lifetime.
func (s *Server) goTask(fn func(ctx context.Context)) {
	s.tasks.Add(1)
	go func() {
		defer s.tasks.Done()
		fn(s.ctx)
	}()
}

// REST (PROTOCOL.md §2).

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(mustJSON(v))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.status())
}

func (s *Server) handleSystems(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildSystems())
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.buildState())
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	n := defaultLogLines
	if v := r.URL.Query().Get("lines"); v != "" {
		var err error
		n, err = strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLogLines {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("lines must be 1..%d", maxLogLines)})
			return
		}
	}
	lines := []string{}
	if s.deps.Log != nil {
		if l := s.deps.Log.Lines(n); l != nil {
			lines = l
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Lines []string `json:"lines"`
	}{lines})
}
