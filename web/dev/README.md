# Web UI development tools

Nothing in this directory is embedded into pdp11d (see `embed.go`); the UI itself is
`web/index.html` and `web/assets/` (plain ES modules, no build step).

Requires Node 20+; `npm install` here installs `ws` (mock server) and, optionally,
`playwright-core` (UI tests).

| Tool | Purpose |
|---|---|
| `mock-server.mjs` | Fake pdp11d speaking `docs/PROTOCOL.md`: serves the real `web/` files with pdp11d's security headers (`Content-Security-Policy`, `X-Frame-Options`), synthetic lamp frames (2.11BSD/RSX idle patterns, the toggle-in chaser; LAMP TEST, key and knob-lamp precedence of `PANEL_LOGIC.md` §4), a fake console (per-system boot transcripts, `login:` or RT-11's `.`, a tiny shell, input held while the CPU is halted), simulator log lines (`log` channel), a systems list with simulated installs, `noLogin`, `bootTimeoutSec` and the boot-timeout toast, the shutdown progress texts, and the panel's console-switch semantics. `node mock-server.mjs --help` lists the options (`--first-run`, `--off`, `--insecure`, `--base /sub/`, `--port`, `--system rt11`, `--boot-hang`, `--boot-timeout N`, `--legacy` for a server without `noLogin`/`bootTimeoutSec`). Test hooks under `/mock/` (see the file header) inject console output, log lines, toasts, state, terminal queries, boot hangs, refused handshakes and dropped connections, and report what the guest received. |
| `tests/*.test.mjs` | UI tests with headless Chromium against the mock: `npm test`. playwright-core comes from `node_modules` or `$PLAYWRIGHT_CORE`; the browser from `$CHROMIUM` or `~/.cache/ms-playwright`. Tests are skipped when either is missing. A Content-Security-Policy violation in any page fails the run; a page load that a network change on the host aborts (`net::ERR_NETWORK_CHANGED`, e.g. a Docker container starting) is retried. Browser tests against the real stack (pdp11d + SimH) are in `tests/e2e/`. |
| `gen-geometry.mjs` | Regenerates `web/assets/panel/geometry.json` from `docs/research/r4-assets/panel1170_elements.json` and `panel-art.json`; `--check` fails if it is out of date. |
| `build-fonts.py` | Rebuilds the woff2 fonts in `web/assets/fonts/` and `panel-art.json` (bezel lettering as SVG paths, label metrics) from pinned, sha256-checked sources: `python3 -I build-fonts.py --download` (needs `fonttools` and `brotli`). The output is reproducible. |
| `panel-preview.html` | The panel alone, without a server (`python3 -m http.server` in the repo root, then `/web/dev/panel-preview.html`). `?photo=<url>&pw=<px>&alpha=0.5` draws r4's BlinkenBone composite photo under the panel in the panel's millimetre frame for visual comparison; `?state=all` lights every lamp. |

Running the real server against the files in this checkout:
`PDP11_WEB=web pdp11d serve` (the UI is then read from disk on every request).
