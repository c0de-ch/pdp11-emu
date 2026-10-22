package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"pdp11emu/server/internal/atomicfile"
)

// controls are the latching front panel controls ($DATA/panel.json).
// Momentary switches are not persisted (they are held by clients).
type controls struct {
	sr       uint32 // SR21..SR0, paddle up = 1
	halt     bool   // ENABLE/HALT paddle down (HALT)
	sbus     bool   // S INST/S BUS CYCLE paddle down (S BUS CYCLE)
	addrKnob int    // 0..7, api.Addr*
	dataKnob int    // 0..7, function = dataKnob % 4 (api.Data*)
	key      string // off | power | lock
}

// defaultControls are the controls at first start (PANEL_LOGIC.md §1).
func defaultControls() controls {
	return controls{addrKnob: 0, dataKnob: 1, key: keyPower}
}

// effectiveHalt is the effect of ENABLE/HALT: LOCK forces ENABLE without
// moving the paddle.
func (c controls) effectiveHalt() bool { return c.halt && c.key != keyLock }

const srMask = 1<<22 - 1

// persistedControls is the JSON form of panel.json.
type persistedControls struct {
	Version    int    `json:"version"`
	SR         uint32 `json:"sr"`
	EnableHalt string `json:"enableHalt"`
	SinstSbus  string `json:"sinstSbus"`
	AddrKnob   int    `json:"addrKnob"`
	DataKnob   int    `json:"dataKnob"`
	Key        string `json:"key"`
}

const stateVersion = 1

func (c controls) persisted() persistedControls {
	return persistedControls{
		Version:    stateVersion,
		SR:         c.sr,
		EnableHalt: latchName(c.halt, "halt", "enable"),
		SinstSbus:  latchName(c.sbus, "bus", "inst"),
		AddrKnob:   c.addrKnob,
		DataKnob:   c.dataKnob,
		Key:        c.key,
	}
}

// loadControls reads the persisted controls. A missing file yields the
// defaults; a damaged file is logged, set aside (".bad") and replaced by
// the defaults, field by field where possible, so that a bad panel.json
// never keeps the appliance from starting. Unreadable files are errors
// (the directory is misconfigured and saving would fail too).
func loadControls(path string, log *slog.Logger) (controls, error) {
	c := defaultControls()
	if path == "" {
		return c, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("panel: %w", err)
	}
	var pc persistedControls
	if err := json.Unmarshal(raw, &pc); err != nil {
		log.Warn("panel state is damaged; using defaults", "path", path, "err", err)
		if rerr := os.Rename(path, path+".bad"); rerr != nil {
			log.Warn("cannot set the damaged panel state aside", "err", rerr)
		}
		return c, nil
	}
	bad := func(field string, v any) {
		log.Warn("invalid value in panel state; using the default", "path", path, "field", field, "value", v)
	}
	if pc.SR&^srMask != 0 {
		bad("sr", pc.SR)
	} else {
		c.sr = pc.SR
	}
	switch pc.EnableHalt {
	case "enable", "halt":
		c.halt = pc.EnableHalt == "halt"
	default:
		bad("enableHalt", pc.EnableHalt)
	}
	switch pc.SinstSbus {
	case "inst", "bus":
		c.sbus = pc.SinstSbus == "bus"
	default:
		bad("sinstSbus", pc.SinstSbus)
	}
	if pc.AddrKnob >= 0 && pc.AddrKnob <= 7 {
		c.addrKnob = pc.AddrKnob
	} else {
		bad("addrKnob", pc.AddrKnob)
	}
	if pc.DataKnob >= 0 && pc.DataKnob <= 7 {
		c.dataKnob = pc.DataKnob
	} else {
		bad("dataKnob", pc.DataKnob)
	}
	switch pc.Key {
	case keyOff, keyPower, keyLock:
		c.key = pc.Key
	default:
		bad("key", pc.Key)
	}
	return c, nil
}

// saveControls writes the controls atomically (a crash leaves either the
// old or the new state).
func saveControls(path string, c controls) error {
	data, err := json.MarshalIndent(c.persisted(), "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(data, '\n'), 0o640)
}

// saveDebounce bounds how often a paddle storm rewrites panel.json.
const saveDebounce = 250 * time.Millisecond

// changedPersistentLocked records a change of a persisted control.
// Caller holds p.mu.
func (p *Panel) changedPersistentLocked() {
	p.saveGen++
	signal(p.saveReq)
}

// saveLoop writes panel.json shortly after the persisted controls changed.
func (p *Panel) saveLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.saveReq:
		}
		select {
		case <-ctx.Done():
			return // Run flushes
		case <-time.After(saveDebounce):
		}
		p.flushSave()
	}
}

// flushSave writes panel.json if a persisted control changed since the
// last successful save.
func (p *Panel) flushSave() {
	if p.opts.StatePath == "" {
		return
	}
	p.saveMu.Lock()
	defer p.saveMu.Unlock()
	p.mu.Lock()
	gen, c := p.saveGen, p.ctl
	p.mu.Unlock()
	if gen == p.savedGen {
		return
	}
	if err := saveControls(p.opts.StatePath, c); err != nil {
		p.log.Error("cannot save the panel state", "path", p.opts.StatePath, "err", err)
		return
	}
	p.savedGen = gen
}
