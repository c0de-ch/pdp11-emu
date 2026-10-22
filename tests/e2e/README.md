# Browser end-to-end tests (real stack)

`real-stack.test.mjs` drives the web UI in headless Chromium against the real
stack: `pdp11d` built from this checkout, the patched SimH and 2.11BSD (the
default system; RT-11 in scenario i), with the UI served from `web/` on disk
(`PDP11_WEB`), so UI changes are tested without rebuilding. The mock-based UI tests are in
`web/dev/tests` (`npm test` there).

The scenarios run in order on one machine, each building on the previous one
(a failed scenario skips the rest):

| | Scenario |
|---|---|
| a | First run on an empty data directory: the overlay shows the install progress, then "Booting…", then the ready banner; `login:` on the console; `root` and `ls /` typed in the browser terminal; the simulator log tab shows SimH's output. |
| b | Front panel with real mouse clicks: RUN lamp lit; ENABLE/HALT → HALT stops the CPU (RUN dark, the stop in the status pill); LOAD ADRS 17777707 + EXAM shows the PC of the stop on the DATA lamps (and the forced ADDRESS pattern); ENABLE + CONT; `echo ok` still answers. |
| c | Toggle-in tutorial: key OFF (confirmation, clean shutdown), HALT, key POWER (powers on halted), authentic speed, the tutorial's auto-toggle; the single lit DATA bit walks through ≥ 6 positions and keeps cycling after it wraps; HALT, LOAD ADRS 1000 and eight EXAMs read back the eight words of `UI.md` §6. |
| d | Pop-out panel window (toolbar button): it receives lamp frames; the main window shows "Bring back" and drops its panel subscription; "Bring back" closes the pop-out over `BroadcastChannel`. |
| e | The page's WebSocket drops: the console history is replayed exactly once. Then pdp11d is restarted (SIGTERM, `PDP11_SHUTDOWN_ON_STOP=0`) on the same data directory: the page reconnects by itself without reloading, the machine boots again (autostart), and the console equals what a freshly connected viewer gets from the history. |
| f | Screenshots while running (main view at 1920×1080 and 1366×768, the panel view with the panel 1340 px wide), then **Shut down OS** from the toolbar: 2.11BSD halts itself, power and key go OFF. |
| g | LAMP TEST precedence: dark with the key OFF; with the key at POWER and the machine off (pdp11d restarted with `PDP11_AUTOSTART=false`, the status pill says "not started") only the two knob lamps of the selected positions are lit, and LAMP TEST lights every lamp; then Boot starts the machine. |
| h | Boot timeout: pdp11d restarted with `PDP11_SYSTEMS` pointing to a copy of the catalog whose 2.11BSD has `bootTimeoutSec` 2: the autostarted boot gets exactly one `warn` toast ("2.11BSD has not reached its prompt after 2 s — check the console. …") and still comes up. |
| i | RT-11 (no shutdown script): installed from the chooser (licence accepted, archive from the cache) and switched to from 2.11BSD ("Pull the plug"); the "no login needed" ready banner; Shut down OS disabled with its reason; Power, Boot, key OFF and a system switch each offer only Cancel (focused) and "Pull the plug", with RT-11's shutdown text; then the plug is pulled. |
| j | Security headers: `/`, `index.html`, assets, `api/status` and a 404 carry the `Content-Security-Policy` of `DESIGN.md` §10 and `X-Frame-Options: SAMEORIGIN`. |

Every page also records Content-Security-Policy violations (the
`securitypolicyviolation` event and CSP console errors): a scenario with any
fails. A page load that a network change on the host aborts
(`net::ERR_NETWORK_CHANGED`, e.g. a Docker container starting) is retried.

## Requirements

Node 20+, Go (to build pdp11d), the patched SimH (`simh/out/pdp11`, see
`simh/build.sh`), `playwright-core` and a Playwright Chromium (headless shell or
full Chromium under `~/.cache/ms-playwright`, or `$CHROMIUM`). No browser is
downloaded by the tests. When something is missing the suite is skipped with
the reason.

## Run

```sh
cd tests/e2e
npm install                      # playwright-core (optional dependency), or:
export PLAYWRIGHT_CORE=/path/to/node_modules/playwright-core
export PDP11_CACHE=/path/to/archive/cache   # optional: archives named by sha256, else 2.11BSD and RT-11 are downloaded (26 MB)
npm test
```

About two minutes with an archive cache. Settings (environment):

| Variable | Default | Meaning |
|---|---|---|
| `E2E_WORK` | new temp dir | work dir: `pdp11d` binary, `data/`, `pdp11d.log`, `timings.json` |
| `E2E_SHOTS` | `$E2E_WORK/shots` | screenshots (`a-…` to `f-…`, `FAILED-<scenario>.png`) |
| `E2E_LISTEN` | `127.0.0.1:<free port>` | pdp11d listen address (127.0.0.1 only) |
| `E2E_PORT_BASE` | a free port | SimH console port (`PDP11_PORT_BASE`) |
| `E2E_SHM` | `$E2E_WORK/shm/panel` | panel export file (`PDP11_SHM`; it is locked, so one per instance) |
| `PDP11_CACHE` | none | read-only archive cache(s), `:`-separated |
| `PDP11_SIMH` | `simh/out/pdp11` | the patched simulator |
| `E2E_BUILD_RETRIES` | `3` | `go build` retries, 30 s apart (a tree being edited may not compile for a moment) |
| `E2E_KEEP` | unset | keep the disks and the binary of a temporary work dir (an explicit `E2E_WORK` is always kept) |

pdp11d gets only these `PDP11_*` settings (plus `PDP11_LOG=debug`), never the
caller's. Everything it starts is stopped at the end; the suite fails if a
simulator outlives pdp11d.
