package web

import (
	"encoding/binary"
	"encoding/json"

	"pdp11emu/server/internal/api"
)

// Binary message types (PROTOCOL.md §1.1).
const (
	binPanelFrame = 0x01
	binConsole    = 0x10
	frameVersion  = 1
)

// Server → client JSON messages (PROTOCOL.md §1.2).

type helloMsg struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
	Server   string `json:"server"`
	ClientID uint64 `json:"clientId"`
	Lamps    int    `json:"lamps"`
	FrameHz  int    `json:"frameHz"`
	Auth     bool   `json:"auth"`
	Insecure bool   `json:"insecure"`
}

type needsInstallMsg struct {
	ID    string `json:"id"`
	Class string `json:"class"`
}

type consoleMsg struct {
	api.ConsoleInfo
	Answerer    uint64 `json:"answerer"`
	InputQueued int    `json:"inputQueued"`
}

type stateMsg struct {
	Type         string           `json:"type"`
	Power        string           `json:"power"`
	Run          api.RunState     `json:"run"`
	Error        string           `json:"error"`
	System       string           `json:"system"`
	Running      string           `json:"running"`
	Speed        api.Speed        `json:"speed"`
	Ready        bool             `json:"ready"`
	NeedsInstall *needsInstallMsg `json:"needsInstall"`
	Controls     api.Controls     `json:"controls"`
	Console      consoleMsg       `json:"console"`
	Stats        api.MachineStats `json:"stats"`
	Viewers      int              `json:"viewers"`
}

type systemsMsg struct {
	Type     string           `json:"type"`
	Selected string           `json:"selected"`
	List     []api.SystemInfo `json:"list"`
}

type progressMsg struct {
	Type string `json:"type"`
	api.Progress
}

type toastMsg struct {
	Type  string `json:"type"`
	Level string `json:"level"`
	Text  string `json:"text"`
	Req   string `json:"req,omitempty"`
}

type logMsg struct {
	Type  string   `json:"type"`
	Lines []string `json:"lines"`
}

type statusMsg struct {
	OK      bool         `json:"ok"`
	Version string       `json:"version"`
	Run     api.RunState `json:"run"`
	System  string       `json:"system"`
	Power   string       `json:"power"`
}

// Client → server JSON (PROTOCOL.md §1.3). Value and Pos carry a number or
// a string depending on the type, so they are decoded per message type.
type clientMsg struct {
	Type          string          `json:"type"`
	Req           string          `json:"req"`
	Sub           []string        `json:"sub"`
	Value         json.RawMessage `json:"value"`
	Name          string          `json:"name"`
	Active        *bool           `json:"active"`
	Pos           json.RawMessage `json:"pos"`
	ID            string          `json:"id"`
	AcceptLicense *bool           `json:"acceptLicense"`
	Task          string          `json:"task"`
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("web: cannot encode message: " + err.Error()) // only our own types are encoded
	}
	return b
}

// frameMessage encodes a panel frame: 01 VV SSSS L0..L63.
func frameMessage(frame *[api.LampCount]byte, seq uint16) []byte {
	b := make([]byte, 4+api.LampCount)
	b[0] = binPanelFrame
	b[1] = frameVersion
	binary.LittleEndian.PutUint16(b[2:], seq)
	copy(b[4:], frame[:])
	return b
}

func (s *Server) buildState() stateMsg {
	m, p, sys := s.deps.Machine, s.deps.Panel, s.deps.Systems
	run := m.State()
	sel := sys.Selected()
	ctl := p.Controls()
	ps := p.PowerStatus()
	st := stateMsg{
		Type:     "state",
		Power:    "on",
		Run:      run,
		System:   sel,
		Running:  m.SystemID(),
		Speed:    sys.SpeedFor(sel),
		Ready:    m.Ready(),
		Controls: ctl,
		Console:  consoleMsg{ConsoleInfo: p.Console()},
		Stats:    m.Stats(),
	}
	if ctl.Key == "off" {
		st.Power = "off"
	}
	if run == api.RunError || ps.Failed {
		st.Error = m.LastError()
	}
	if ps.NeedsInstall != "" {
		ni := &needsInstallMsg{ID: ps.NeedsInstall}
		if info, ok := sys.Get(ps.NeedsInstall); ok {
			ni.Class = info.License.Class
		}
		st.NeedsInstall = ni
	}
	if c := m.Console(); c != nil {
		st.Console.InputQueued = c.Queued()
	}
	st.Viewers, st.Console.Answerer = s.hub.viewers()
	return st
}

func (s *Server) buildSystems() systemsMsg {
	list := s.deps.Systems.List()
	if list == nil {
		list = []api.SystemInfo{}
	}
	for i := range list {
		if list[i].Credentials == nil {
			list[i].Credentials = []api.Credential{}
		}
	}
	return systemsMsg{Type: "systems", Selected: s.deps.Systems.Selected(), List: list}
}

func (s *Server) status() statusMsg {
	power := "on"
	if s.deps.Panel.Controls().Key == "off" {
		power = "off"
	}
	return statusMsg{OK: true, Version: s.opts.Version, Run: s.deps.Machine.State(),
		System: s.deps.Systems.Selected(), Power: power}
}
