# Third-party software and data

pdp11-emu's own code (pdp11d, the web UI, the SimH patches and the panel
device, the catalog, the scripts) is under the MIT licence: [`LICENSE`](LICENSE),
"Copyright (c) 2026 The pdp11-emu authors".

The binaries and the Docker image also contain the components below. Their
licence texts ship with the binaries, collected by
[`deploy/collect-licenses.sh`](deploy/collect-licenses.sh): in
`/opt/pdp11/share/doc/licenses/` of the Docker image and of a native install
(this file and `LICENSE` are in `/opt/pdp11/share/doc/`), and in `licenses/`
of a `make dist` tarball.

## Software

| Component | Version | Licence | Where it ends up |
|---|---|---|---|
| [Open SIMH](https://github.com/open-simh/simh) (PDP-11 simulator) | commit `87eb7d5e96f9` (2026-09-22, [`simh/SIMH_COMMIT`](simh/SIMH_COMMIT)) plus [`simh/patches/`](simh/patches) and [`simh/panel/`](simh/panel) | X11-style licence, "Copyright (c) Robert M Supnik and the Open SIMH contributors" (`LICENSE.txt` of the SimH tree; the patches are offered under the same terms) | the `pdp11` binary |
| Slirp, bundled with Open SIMH (its NAT network transport, compiled in, not used by the appliance) | as in the SimH commit | BSD 2-Clause, Copyright (c) 1995, 1996 Danny Gasparovski and others (`slirp/COPYRIGHT` of the SimH tree) | the `pdp11` binary |
| [xterm.js](https://github.com/xtermjs/xterm.js) (`@xterm/xterm`) | 6.0.0 | MIT, "Copyright (c) 2017-2019, The xterm.js authors", 2014-2016 SourceLair, 2012-2013 Christopher Jeffrey ([`web/assets/vendor/xterm/LICENSE.txt`](web/assets/vendor/xterm/LICENSE.txt)) | the web UI, embedded in `pdp11d` |
| [Glass TTY VT220](https://github.com/svofski/glasstty) font, Viacheslav Slavinsky | commit `2bed112c` | The Unlicense (public domain) ([`LICENSE-glass-tty-vt220.txt`](web/assets/fonts/LICENSE-glass-tty-vt220.txt)) | `web/assets/fonts/glass-tty-vt220.woff2` (console) |
| [VT323](https://github.com/google/fonts/tree/main/ofl/vt323) font, Peter Hull | google/fonts `45e3d526` | SIL Open Font License 1.1 ([`LICENSE-vt323-OFL.txt`](web/assets/fonts/LICENSE-vt323-OFL.txt)) | `web/assets/fonts/vt323.woff2` (console fallback, subset) |
| [Archivo](https://github.com/Omnibus-Type/Archivo) font, Omnibus-Type | google/fonts `45e3d526` | SIL Open Font License 1.1 ([`LICENSE-archivo-OFL.txt`](web/assets/fonts/LICENSE-archivo-OFL.txt)) | `web/assets/fonts/archivo-panel*.woff2` (panel labels; instanced and subset) |
| [Montserrat Alternates](https://github.com/JulietaUla/Montserrat) font, The Montserrat Project Authors | google/fonts `45e3d526` | SIL Open Font License 1.1 ([`LICENSE-montserrat-alternates-OFL.txt`](web/assets/fonts/LICENSE-montserrat-alternates-OFL.txt)) | glyph outlines converted to SVG paths for the bezel lettering, in `web/assets/panel/geometry.json` |
| [github.com/coder/websocket](https://github.com/coder/websocket) | v1.8.15 | ISC, "Copyright (c) 2025 Coder" | linked into `pdp11d` |
| [github.com/ulikunitz/xz](https://github.com/ulikunitz/xz) | v0.5.17 | BSD 3-Clause, "Copyright (c) 2014-2022 Ulrich Kunitz" | linked into `pdp11d` |
| [Go](https://go.dev) standard library and runtime | the Go release used to build (1.27) | BSD 3-Clause, "Copyright 2009 The Go Authors" | linked into `pdp11d` |

The fonts and their sources are pinned by sha256 in
[`web/dev/build-fonts.py`](web/dev/build-fonts.py); the provenance of the
vendored files is in [`web/assets/README.txt`](web/assets/README.txt) and
[`web/assets/vendor/xterm/README.txt`](web/assets/vendor/xterm/README.txt).

The Docker image is based on `debian:trixie-slim`; its packages carry their own
licences (`/usr/share/doc/*/copyright`). The CA certificate bundle
`/etc/ssl/certs/ca-certificates.crt` is copied from Debian's `ca-certificates`
package (certificate data from Mozilla, MPL 2.0); that package's copyright
file, with the MPL 2.0 text, is `licenses/ca-certificates.txt`. The image's
`org.opencontainers.image.licenses` label lists the licences of the software
above and of the CA bundle.

## Operating system images

No disk image is part of this repository. pdp11d downloads each system from
its original source when it is installed (sha256-verified), and shows the
system's licence before the install; hobbyist licences must be accepted
explicitly. The licence texts are in `systems/<id>/LICENSE.txt` and in the UI
(Help, System).

| System | Licence | Class | Source |
|---|---|---|---|
| 2.11BSD patch level 482 (`bsd211`, default) | University of California BSD licence + Caldera "Ancient UNIX" licence (2002) | redistributable | Chase Covello's PiDP-11 distribution, `github.com/chasecovello/211bsd-pidp11` (pinned commit) |
| 2.11BSD patch level 499 (`bsd211-pl499`) | BSD licence + Caldera licence | redistributable | Johnny Billquist, `ftp.dfupdate.se` |
| UNIX V7 (`unixv7`) | Caldera "Ancient UNIX" licence (2002) | redistributable | w11 OS kit, `www.retro11.de` (Internet Archive mirror) |
| UNIX V6 (`unixv6`), UNIX V5 (`unixv5`) | Caldera "Ancient UNIX" licence (2002) | redistributable | SimH software kits, `simh.trailing-edge.com` (Internet Archive mirror) |
| RT-11 V5.3 (`rt11`) | Mentec PDP-11 hobbyist licence | hobbyist | SimH software kit, `simh.trailing-edge.com` |
| RSX-11M-PLUS V3.0 (`rsx11mp`) | Mentec PDP-11 hobbyist licence | hobbyist | w11 OS kit, `www.retro11.de` |
| RSTS/E V7.0 (`rsts`) | Mentec PDP-11 hobbyist licence | hobbyist | SimH software kit, `simh.trailing-edge.com` |

Images built from this repository contain at most the archives of
**redistributable** systems (the Docker build's `PREFETCH`; by default only
2.11BSD). Hobbyist-licensed systems are only ever downloaded by the user on
their own machine, after accepting the licence; they are never redistributed in
images or caches built by this project.

## Development tools (not shipped)

`web/dev/` uses [ws](https://github.com/websockets/ws) 8.22.0 (MIT) for the
mock server and, optionally, [playwright-core](https://github.com/microsoft/playwright)
(Apache 2.0) for the UI tests; `web/dev/build-fonts.py` uses fontTools (MIT)
and Brotli (MIT). The build uses the official `golang` and Debian images and
Debian's compilers; none of them is part of the runtime image beyond what is
listed above.
