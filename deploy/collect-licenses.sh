#!/usr/bin/env bash
# collect-licenses.sh: the licence texts of the third-party code in the pdp11
# and pdp11d binaries (THIRD_PARTY.md), for everything that ships them: the
# Docker image, "make dist" tarballs and native installs
# (/opt/pdp11/share/doc/licenses).
#
#   deploy/collect-licenses.sh [--simh-src DIR] OUT
#
#   --simh-src DIR   the SimH work tree the simulator was built from (default
#                    simh/src); only LICENSE.txt and slirp/COPYRIGHT are read.
#
# OUT (a new or empty directory) receives
#   open-simh.txt, open-simh-slirp.txt   the SimH tree's LICENSE.txt and slirp/COPYRIGHT
#   go.txt                               the licence of the Go toolchain ($GO env GOROOT)
#   github.com_<owner>_<name>.txt        LICENSE of every module linked into pdp11d
#   xterm.js.txt, font-<name>.txt        the web UI's vendored licence files
#   systems/<id>.txt                     the licence of every catalog system
# Go runs as $GO (default go) with the caller's environment (GOPATH, GOFLAGS,
# GOTOOLCHAIN, ...): the modules must be in its module cache or downloadable.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
simh_src=$root/simh/src
go=${GO:-go}

die() { printf 'collect-licenses.sh: error: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p}' "$0"; }

need_arg() { [ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value"; }
while [ $# -gt 0 ]; do
	case $1 in
	--simh-src) need_arg "$@"; simh_src=$2; shift 2 ;;
	--simh-src=*) simh_src=${1#*=}; shift ;;
	-h | --help) usage; exit 0 ;;
	-*) usage >&2; die "unknown argument: $1" ;;
	*) break ;;
	esac
done
[ $# -eq 1 ] || { usage >&2; die "one output directory needed"; }
out=$1

put() { install -m 0644 "$1" "$out/$2"; }

install -d -m 0755 "$out" "$out/systems"
for f in LICENSE.txt slirp/COPYRIGHT; do
	[ -f "$simh_src/$f" ] || die "$simh_src/$f is missing (--simh-src: the SimH work tree)"
done
put "$simh_src/LICENSE.txt" open-simh.txt
put "$simh_src/slirp/COPYRIGHT" open-simh-slirp.txt

cd "$root"
# An upstream Go has GOROOT/LICENSE; Debian's and Ubuntu's golang packages
# drop it, and their copyright file carries the licence instead.
goroot=$("$go" env GOROOT)
golicense=$goroot/LICENSE
if [ ! -f "$golicense" ] && pkg=$(dpkg -S "$(readlink -f "$goroot/bin/go")" 2>/dev/null); then
	golicense=/usr/share/doc/${pkg%%:*}/copyright
fi
[ -f "$golicense" ] || die "no licence file of the Go toolchain in $goroot"
put "$golicense" go.txt
modules=$("$go" list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}' ./server/cmd/pdp11d | sort -u)
for m in $modules; do
	dir=$("$go" list -m -f '{{.Dir}}' "$m")
	lic=
	for f in "$dir"/LICENSE "$dir"/LICENSE.* "$dir"/COPYING; do
		if [ -f "$f" ]; then lic=$f; break; fi
	done
	[ -n "$lic" ] || die "module $m has no LICENSE file in ${dir:-the module cache}"
	put "$lic" "$(printf '%s' "$m" | tr / _).txt"
done

put web/assets/vendor/xterm/LICENSE.txt xterm.js.txt
for f in web/assets/fonts/LICENSE-*.txt; do
	put "$f" "font-${f#web/assets/fonts/LICENSE-}"
done
for f in systems/*/LICENSE.txt; do
	id=${f#systems/}
	put "$f" "systems/${id%/LICENSE.txt}.txt"
done
