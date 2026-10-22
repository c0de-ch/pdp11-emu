// Package pdp11emu embeds the static web UI and the default system catalog
// so that pdp11d is a single self-contained binary.
package pdp11emu

import "embed"

// Web holds web/index.html and web/assets/... (served at / and /assets/...).
//
//go:embed web/index.html web/assets
var Web embed.FS

// Systems holds the default catalog: systems/<id>/system.json, boot.ini, licence texts.
//
//go:embed systems
var Systems embed.FS
