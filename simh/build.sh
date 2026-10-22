#!/usr/bin/env bash
# simh/build.sh: build the patched Open SIMH PDP-11 simulator (docs/SIMH_PATCH.md).
#
#   simh/build.sh [--src DIR] [--out DIR] [--jobs N] [--repo URL]
#                 [--baseline] [--no-smoke]
#
#   --src DIR    SimH work tree (default simh/src).  Created and fetched if
#                missing; an existing clone is reused (no network needed when
#                it already has the pinned commit) and reset to a clean state.
#   --out DIR    install DIR/pdp11 (default simh/out), atomically.
#   --jobs N     make -j N (default: number of CPUs).
#   --repo URL   where to fetch the commit from (default $SIMH_REPO or
#                https://github.com/open-simh/simh; a local clone works too).
#   --baseline   build the pinned commit with only the non-panel patches (the
#                unpatched CPU, for overhead measurements).
#   --no-smoke   skip the smoke check (cross builds whose binary cannot run).
#
# Steps: fetch the commit in simh/SIMH_COMMIT, verify HEAD, reset and clean
# the tree, "git apply" simh/patches/*.patch in order, copy simh/panel/* into
# PDP11/, "make TESTS=0 pdp11", smoke-check the binary (a "sim> " prompt on a
# pipe and a PANEL device) and rename it into place.
#
# Environment: CC is passed to SimH's makefile as GCC=, CFLAGS as OPTIMIZE=
# (replacing its "-O2 -DNDEBUG=1"), LDFLAGS as LDFLAGS_O= (appended to the
# link).  Needs bash, git, make, a C compiler and, to fetch over HTTPS,
# ca-certificates.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
src=$here/src
out=$here/out
jobs=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)
repo=${SIMH_REPO:-https://github.com/open-simh/simh}
baseline=0
smoke=1

log() { printf 'build.sh: %s\n' "$*" >&2; }
die() { printf 'build.sh: error: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p}' "$0"; }

need_arg() { [ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value"; }
while [ $# -gt 0 ]; do
    case $1 in
    --src) need_arg "$@"; src=$2; shift 2 ;;
    --src=*) src=${1#*=}; shift ;;
    --out) need_arg "$@"; out=$2; shift 2 ;;
    --out=*) out=${1#*=}; shift ;;
    --jobs) need_arg "$@"; jobs=$2; shift 2 ;;
    --jobs=*) jobs=${1#*=}; shift ;;
    --repo) need_arg "$@"; repo=$2; shift 2 ;;
    --repo=*) repo=${1#*=}; shift ;;
    --baseline) baseline=1; shift ;;
    --no-smoke) smoke=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown argument: $1" ;;
    esac
done
case $jobs in ''|*[!0-9]*|0) die "--jobs must be a positive number" ;; esac

for tool in git make; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed"
done

commit=$(tr -d '[:space:]' < "$here/SIMH_COMMIT")
case $commit in
*[!0-9a-f]*|'') die "SIMH_COMMIT does not hold a commit id" ;;
esac
[ ${#commit} -eq 40 ] || die "SIMH_COMMIT must hold a full 40-digit commit id"

mkdir -p "$src" "$out"
src=$(cd "$src" && pwd)
out=$(cd "$out" && pwd)
g() { git -C "$src" "$@"; }

# 1. The pinned commit, in a clean tree.
if [ ! -d "$src/.git" ]; then
    [ -z "$(ls -A "$src")" ] || die "$src is not empty and not a git work tree"
    log "creating $src"
    git init -q "$src"
fi
if ! g cat-file -e "$commit^{commit}" 2>/dev/null; then
    log "fetching $commit from $repo"
    if ! g fetch -q --depth 1 "$repo" "$commit"; then
        log "fetching the commit alone failed; fetching all of $repo"
        g fetch -q "$repo" '+refs/heads/*:refs/remotes/build/*' ||
            die "cannot fetch $repo (network access? ca-certificates for HTTPS?)"
    fi
    g cat-file -e "$commit^{commit}" 2>/dev/null || die "$repo has no commit $commit"
fi
g -c advice.detachedHead=false checkout -q -f --detach "$commit"
g reset -q --hard "$commit"
g clean -q -ffdx
[ "$(g rev-parse HEAD)" = "$commit" ] || die "HEAD of $src is not $commit"
log "source: $src at $commit"

# 2. Patches and the panel sources.
shopt -s nullglob
patches=("$here"/patches/*.patch)
shopt -u nullglob
[ ${#patches[@]} -gt 0 ] || die "no patches in $here/patches"
for p in "${patches[@]}"; do
    name=$(basename "$p")
    if [ "$baseline" = 1 ] && [[ $name == *panel* ]]; then
        log "baseline: skipping $name"
        continue
    fi
    g apply --whitespace=nowarn "$p" || die "$name does not apply to $commit"
    log "applied $name"
done
if [ "$baseline" = 0 ]; then
    cp "$here"/panel/*.[ch] "$src/PDP11/"
    log "copied panel/*.[ch] into PDP11/"
fi

# 3. Build.
make_args=(TESTS=0)
[ -z "${CC:-}" ] || make_args+=("GCC=$CC")
[ -z "${CFLAGS:-}" ] || make_args+=("OPTIMIZE=$CFLAGS")
[ -z "${LDFLAGS:-}" ] || make_args+=("LDFLAGS_O=$LDFLAGS")
log "make -j$jobs ${make_args[*]} pdp11"
rm -f "$src/BIN/pdp11"
make -C "$src" -j"$jobs" "${make_args[@]}" pdp11
[ -x "$src/BIN/pdp11" ] || die "the build produced no BIN/pdp11"

# 4. Smoke check on a copy next to the target, then rename it into place.
tmp=$(mktemp "$out/.pdp11.XXXXXX")
work=$(mktemp -d "$out/.smoke.XXXXXX")
trap 'rm -rf "$tmp" "$work"' EXIT
cp "$src/BIN/pdp11" "$tmp"
chmod 0755 "$tmp"
if [ "$smoke" = 1 ]; then
    # A pipe on stdin, an empty cwd and HOME (no pdp11.ini is picked up).
    if ! transcript=$(cd "$work" && printf 'show version\nshow panel\nexit\n' |
            HOME=$work timeout 60 "$tmp" 2>&1); then
        printf '%s\n' "$transcript" >&2
        die "smoke check: the simulator failed"
    fi
    case $transcript in
    *'sim> '*) ;;
    *) printf '%s\n' "$transcript" >&2; die "smoke check: no 'sim> ' prompt on a pipe" ;;
    esac
    if [ "$baseline" = 0 ]; then
        case $transcript in
        *PANEL*'not attached'*) ;;
        *) printf '%s\n' "$transcript" >&2; die "smoke check: no PANEL device" ;;
        esac
    fi
    log "smoke check passed: $(printf '%s\n' "$transcript" | grep -m 1 'simulator' || true)"
fi
mv -f "$tmp" "$out/pdp11"
log "installed $out/pdp11"
