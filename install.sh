#!/usr/bin/env bash
# install.sh: install the PDP-11/70 appliance (pdp11d and the patched Open
# SIMH) on Debian 12/13 or Ubuntu 22.04/24.04 (amd64, arm64), in a VM or in an
# unprivileged LXC container. README.md "Install in a VM or LXC container",
# docs/PROXMOX.md.
#
#   sudo ./install.sh [--port N] [--no-start] [--no-systemd]
#                     [--prefetch default|all|none] [--archives DIR]
#   sudo ./install.sh --uninstall [--purge]
#
# Run it as root from a checkout of this project. It
#   1. installs the build tools (build-essential git ca-certificates, and
#      curl when a Go toolchain has to be downloaded);
#   2. uses the system's Go if it is new enough for go.mod, else downloads the
#      official Go release pinned below (sha256 checked);
#   3. builds SimH (simh/build.sh, fetching the pinned Open SIMH commit from
#      GitHub) and pdp11d from this checkout, in a work directory under
#      /opt/pdp11 that is removed afterwards (not in /tmp or /var/tmp, which
#      hardened hosts mount noexec);
#   4. installs /opt/pdp11/bin/{pdp11,pdp11d,pdp11d-run}, the licence texts
#      (/opt/pdp11/share/doc/licenses) and the archive of the default system
#      into /opt/pdp11/cache;
#   5. creates the system user pdp11 (home and data directory /var/lib/pdp11);
#   6. writes /etc/pdp11/pdp11d.env from deploy/pdp11d.env unless it exists;
#   7. installs deploy/pdp11d.service, enables and (re)starts it, and checks
#      that it keeps running;
#   8. prints the URL.
# Running it again upgrades: it rebuilds, replaces the binaries and restarts
# the service (a running guest OS is shut down cleanly first); settings and
# data are kept. A tree without sources but with bin/pdp11 and bin/pdp11d
# (a "make dist" tarball) is installed as it is, without build tools.
#
# Options:
#   --port N          HTTP port: PDP11_LISTEN=:N in the settings file (default 8080)
#   --no-start        install and enable the service, but do not (re)start it
#   --no-systemd      install the files only, call no systemctl (containers
#                     without systemd); start /opt/pdp11/bin/pdp11d-run yourself
#   --prefetch WHAT   archives for /opt/pdp11/cache: default (the catalog's
#                     default system), all (every redistributable system), none
#   --archives DIR    take the archives from DIR (matched by size and sha256)
#                     instead of downloading them
#   --uninstall       stop and remove the service and /opt/pdp11; keeps the
#                     settings (/etc/pdp11) and the data (/var/lib/pdp11)
#   --purge           with --uninstall: also remove the settings, the data
#                     (all disks of all systems) and the user pdp11 if
#                     install.sh created it
set -euo pipefail

# Go release used when the system has no suitable Go (https://go.dev/dl/).
GO_VERSION=1.27.3
GO_SHA256_amd64=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40
GO_SHA256_arm64=a06e99309dbcbdca1a8ccd2282c3ee146e55930886e26b4f1193269587165f49

PREFIX=/opt/pdp11
BINDIR=$PREFIX/bin
CACHEDIR=$PREFIX/cache
DOCDIR=$PREFIX/share/doc
ETCDIR=/etc/pdp11
ENV_FILE=$ETCDIR/pdp11d.env
DATADIR=/var/lib/pdp11
UNIT=/etc/systemd/system/pdp11d.service
SVC_USER=pdp11
SVC_COMMENT="PDP-11/70 appliance"

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
port=
start=1
systemd=1
prefetch=default
archives=
uninstall=0
purge=0
work=

log() { printf 'install.sh: %s\n' "$*" >&2; }
warn() { printf 'install.sh: warning: %s\n' "$*" >&2; }
die() { printf 'install.sh: error: %s\n' "$*" >&2; exit 1; }
usage() { sed -n '2,/^set -euo/{/^set -euo/d;s/^# \{0,1\}//;p}' "$0"; }

need_arg() { [ $# -ge 2 ] && [ -n "$2" ] || die "$1 needs a value"; }
while [ $# -gt 0 ]; do
	case $1 in
	--port) need_arg "$@"; port=$2; shift 2 ;;
	--port=*) port=${1#*=}; shift ;;
	--no-start) start=0; shift ;;
	--no-systemd) systemd=0; shift ;;
	--prefetch) need_arg "$@"; prefetch=$2; shift 2 ;;
	--prefetch=*) prefetch=${1#*=}; shift ;;
	--archives) need_arg "$@"; archives=$2; shift 2 ;;
	--archives=*) archives=${1#*=}; shift ;;
	--uninstall) uninstall=1; shift ;;
	--purge) purge=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown argument: $1" ;;
	esac
done
if [ -n "$port" ]; then
	case $port in '' | *[!0-9]*) die "--port must be a number" ;; esac
	[ "$port" -ge 1 ] && [ "$port" -le 65535 ] || die "--port must be in 1..65535"
fi
case $prefetch in default | all | none) ;; *) die "--prefetch must be default, all or none" ;; esac
if [ -n "$archives" ]; then
	[ -d "$archives" ] || die "--archives: $archives is not a directory"
	archives=$(cd "$archives" && pwd)
fi
[ "$purge" = 0 ] || [ "$uninstall" = 1 ] || die "--purge only goes with --uninstall"
[ "$(id -u)" = 0 ] || die "run me as root (sudo $0)"

# systemd is used when it runs as the init system, unless --no-systemd.
use_systemd() { [ "$systemd" = 1 ] && [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; }

cleanup() {
	[ -z "$work" ] || rm -rf "$work"
	# Left empty by a first install that failed (made for the work directory).
	rmdir "$PREFIX" 2>/dev/null || true
}
trap cleanup EXIT

# own_user: whether the account $SVC_USER is the one install.sh creates (a
# system account with home $DATADIR, no login shell and $SVC_COMMENT). An
# account of that name made by someone else is used, but never removed.
own_user() {
	local ent uid gecos home shell sys_max
	ent=$(getent passwd "$SVC_USER") || return 1
	IFS=: read -r _ _ uid _ gecos home shell <<<"$ent"
	sys_max=$(awk '$1 == "SYS_UID_MAX" { print $2 }' /etc/login.defs 2>/dev/null)
	[ "$uid" -le "${sys_max:-999}" ] && [ "$home" = "$DATADIR" ] && [ "$gecos" = "$SVC_COMMENT" ] &&
		case $shell in */nologin | */false) true ;; *) false ;; esac
}

# ---- uninstall ---------------------------------------------------------------

# pids of the pdp11d processes started as $BINDIR/pdp11d (by systemd,
# pdp11d-run or by hand). This compares argv[0]: /proc/<pid>/exe of another
# user's process is unreadable for root without CAP_SYS_PTRACE (containers).
pdp11d_pids() {
	local p arg0
	for p in /proc/[0-9]*; do
		arg0=
		{ IFS= read -r -d '' arg0 <"$p/cmdline"; } 2>/dev/null || true
		if [ "$arg0" = "$BINDIR/pdp11d" ]; then echo "${p#/proc/}"; fi
	done
}

# stop_manual: SIGTERM pdp11d processes not run by systemd and wait for them
# (the guest OS shutdown takes up to 75 s).
stop_manual() {
	local pids pid i
	pids=$(pdp11d_pids)
	[ -n "$pids" ] || return 0
	log "stopping pdp11d (pid $(echo $pids)); the guest OS is shut down first"
	for pid in $pids; do kill -TERM "$pid" 2>/dev/null || true; done
	for ((i = 0; i < 90; i++)); do
		[ -n "$(pdp11d_pids)" ] || return 0
		sleep 1
	done
	warn "pdp11d did not stop within 90 s; killing it"
	for pid in $(pdp11d_pids); do kill -KILL "$pid" 2>/dev/null || true; done
}

do_uninstall() {
	if use_systemd; then
		if systemctl is-active --quiet pdp11d.service || systemctl is-enabled --quiet pdp11d.service 2>/dev/null; then
			log "stopping and disabling pdp11d.service (the guest OS is shut down first)"
			systemctl disable --now pdp11d.service || warn "could not stop pdp11d.service"
		fi
	fi
	stop_manual
	if [ -e "$UNIT" ]; then
		rm -f "$UNIT"
		if use_systemd; then systemctl daemon-reload; fi
	fi
	rm -rf "$PREFIX"
	log "removed $PREFIX and $UNIT"
	if [ "$purge" = 1 ]; then
		rm -rf "$ETCDIR" "$DATADIR" /run/pdp11
		log "purged $ETCDIR and $DATADIR"
		if own_user; then
			userdel "$SVC_USER" || warn "could not remove the user $SVC_USER"
			if getent group "$SVC_USER" >/dev/null; then groupdel "$SVC_USER" 2>/dev/null || true; fi
			log "removed the user $SVC_USER"
		elif getent passwd "$SVC_USER" >/dev/null; then
			warn "kept the account $SVC_USER: install.sh did not create it"
		fi
	else
		log "kept the settings ($ETCDIR), the data ($DATADIR) and the user $SVC_USER; --uninstall --purge removes them"
	fi
}

if [ "$uninstall" = 1 ]; then
	do_uninstall
	exit 0
fi

# ---- checks -----------------------------------------------------------------

[ -r /etc/os-release ] || die "cannot identify the operating system (no /etc/os-release)"
# shellcheck disable=SC1091
. /etc/os-release
case "${ID:-}:${VERSION_ID:-}" in
debian:12 | debian:13 | ubuntu:22.04 | ubuntu:24.04) ;;
*)
	case " ${ID:-} ${ID_LIKE:-} " in
	*" debian "* | *" ubuntu "*) warn "${PRETTY_NAME:-this system} is not tested (Debian 12/13 and Ubuntu 22.04/24.04 are); trying anyway" ;;
	*) die "${PRETTY_NAME:-this system} is not supported: Debian 12/13 or Ubuntu 22.04/24.04 needed" ;;
	esac
	;;
esac
arch=$(dpkg --print-architecture)

if [ -f "$here/go.mod" ] && [ -f "$here/simh/build.sh" ]; then
	mode=build
elif [ -x "$here/bin/pdp11" ] && [ -x "$here/bin/pdp11d" ]; then
	mode=prebuilt
else
	die "$here has neither the sources (go.mod, simh/build.sh) nor prebuilt binaries (bin/pdp11, bin/pdp11d)"
fi
for f in deploy/pdp11d.service deploy/pdp11d.env deploy/pdp11d-run; do
	[ -f "$here/$f" ] || die "$here/$f is missing"
done

# The work directory holds the Go toolchain and the SimH tree, whose build
# runs helpers from there: under $PREFIX, where the binaries have to run
# anyway (hardened hosts mount /tmp and /var/tmp noexec).
install -d -m 0755 "$PREFIX"
work=$(mktemp -d "$PREFIX/.install.XXXXXX")
printf '#!/bin/sh\n' >"$work/exec-check"
chmod 0755 "$work/exec-check"
if ! "$work/exec-check" 2>/dev/null; then
	die "cannot run programs in $PREFIX, where the build and the installed binaries run: its file system must not be mounted noexec (mount options: $(findmnt -no OPTIONS --target "$PREFIX" 2>/dev/null || echo unknown))"
fi

# ---- build ------------------------------------------------------------------

version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n 1)" = "$2" ]; }

# find_go sets GO to a go binary that is at least the go.mod version.
find_go() {
	local g v
	for g in "$(command -v go 2>/dev/null || true)" /usr/local/go/bin/go /usr/lib/go/bin/go; do
		[ -n "$g" ] && [ -x "$g" ] || continue
		v=$(GOTOOLCHAIN=local "$g" version 2>/dev/null | awk '{ print $3 }') || continue
		v=${v#go}
		if [ -n "$v" ] && version_ge "$v" "$go_need"; then
			GO=$g
			log "using $g (go$v; go.mod needs $go_need)"
			return 0
		fi
		log "ignoring $g (go$v; go.mod needs $go_need)"
	done
	return 1
}

download_go() {
	local sum url
	case $arch in
	amd64) sum=$GO_SHA256_amd64 ;;
	arm64) sum=$GO_SHA256_arm64 ;;
	*) die "no Go $go_need or newer, and no pinned Go download for $arch: install Go and re-run" ;;
	esac
	url=https://go.dev/dl/go$GO_VERSION.linux-$arch.tar.gz
	log "downloading $url"
	curl -fsSL --retry 3 --connect-timeout 20 -o "$work/go.tar.gz" "$url" || die "cannot download $url"
	echo "$sum  $work/go.tar.gz" | sha256sum -c --quiet - || die "sha256 of $url does not match"
	tar -C "$work" -xzf "$work/go.tar.gz"
	rm -f "$work/go.tar.gz"
	GO=$work/go/bin/go
	log "using go$GO_VERSION in $work/go (removed afterwards)"
}

apt_install() {
	log "installing $*"
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -q
	apt-get install -y -q --no-install-recommends "$@"
}

build() {
	local pkgs=(build-essential git ca-certificates) ver ldflags
	local goenv=(env GOTOOLCHAIN=local GOFLAGS=-modcacherw GOPATH="$work/gopath" GOCACHE="$work/gocache")
	go_need=$(awk '$1 == "go" { print $2; exit }' "$here/go.mod")
	[ -n "$go_need" ] || die "no go directive in $here/go.mod"
	GO=
	find_go || pkgs+=(curl)
	apt_install "${pkgs[@]}"
	[ -n "$GO" ] || download_go

	log "building SimH (fetches the pinned Open SIMH commit)"
	bash "$here/simh/build.sh" --src "$work/simh-src" --out "$work/bin"

	log "building pdp11d"
	ldflags="-s -w"
	if ver=$(git -c safe.directory="$here" -C "$here" describe --tags --always --dirty 2>/dev/null); then
		ldflags+=" -X main.version=$ver"
	fi
	(cd "$here" && "${goenv[@]}" CGO_ENABLED=0 "$GO" build -trimpath -buildvcs=false -ldflags "$ldflags" \
		-o "$work/bin/pdp11d" ./server/cmd/pdp11d)

	# The licence texts need the SimH tree and the module cache of the build.
	"${goenv[@]}" GO="$GO" bash "$here/deploy/collect-licenses.sh" --simh-src "$work/simh-src" "$work/licenses"
	rm -rf "$work/simh-src"
	src=$work/bin
	licenses=$work/licenses
}

if [ "$mode" = build ]; then
	build
else
	log "installing the prebuilt binaries from $here/bin"
	"$here/bin/pdp11d" version >/dev/null 2>&1 || die "$here/bin/pdp11d does not run here (wrong architecture?)"
	# The simulator links glibc dynamically: it needs the build machine's
	# glibc version or a newer one.
	if ! out=$(cd "$work" && printf 'exit\n' | HOME=$work "$here/bin/pdp11" 2>&1); then
		die "$here/bin/pdp11 does not run here (build the tarball on a system with an older glibc, or install from the sources): $(printf '%s' "$out" | head -n 3)"
	fi
	# pdp11d downloads over HTTPS at run time (gzip, the other runtime tool,
	# is an essential package).
	if ! dpkg-query -W -f '${Status}' ca-certificates 2>/dev/null | grep -q 'ok installed'; then
		apt_install ca-certificates
	fi
	src=$here/bin
	licenses=$here/licenses
fi

# ---- files ------------------------------------------------------------------

install -d -m 0755 "$PREFIX" "$BINDIR" "$CACHEDIR" "$DOCDIR"
for f in pdp11 pdp11d; do
	# A new file renamed into place: a running pdp11d keeps its binary.
	install -m 0755 "$src/$f" "$BINDIR/.$f.new"
	mv -f "$BINDIR/.$f.new" "$BINDIR/$f"
done
install -m 0755 "$here/deploy/pdp11d-run" "$BINDIR/pdp11d-run"
for f in LICENSE THIRD_PARTY.md README.md; do
	[ ! -f "$here/$f" ] || install -m 0644 "$here/$f" "$DOCDIR/$f"
done
if [ -d "$licenses" ]; then
	rm -rf "$DOCDIR/licenses"
	cp -R "$licenses" "$DOCDIR/licenses"
	chmod -R u=rwX,go=rX "$DOCDIR/licenses"
else
	warn "$here has no licenses/ directory: the third-party licence texts are not installed"
fi
log "installed $BINDIR/pdp11 and $BINDIR/pdp11d ($("$BINDIR/pdp11d" version))"

if ! getent group "$SVC_USER" >/dev/null; then groupadd --system "$SVC_USER"; fi
if ! getent passwd "$SVC_USER" >/dev/null; then
	useradd --system --gid "$SVC_USER" --home-dir "$DATADIR" --no-create-home \
		--shell /usr/sbin/nologin --comment "$SVC_COMMENT" "$SVC_USER"
	log "created the system user $SVC_USER"
elif ! own_user; then
	IFS=: read -r _ _ uid _ _ _ shell <<<"$(getent passwd "$SVC_USER")"
	case $shell in
	*/nologin | */false) log "using the existing account $SVC_USER (uid $uid)" ;;
	*) warn "the existing account $SVC_USER (uid $uid) is a login account ($shell): pdp11d runs as it, with all its groups; --purge keeps it" ;;
	esac
fi
install -d -m 0750 -o "$SVC_USER" -g "$SVC_USER" "$DATADIR"

install -d -m 0755 "$ETCDIR"
new_env=0
if [ ! -e "$ENV_FILE" ]; then
	install -m 0640 -o root -g "$SVC_USER" "$here/deploy/pdp11d.env" "$ENV_FILE"
	new_env=1
	log "wrote $ENV_FILE"
fi
if [ -n "$port" ]; then
	# Replace the first PDP11_LISTEN line (also a commented one), else append.
	if grep -qE '^[#[:space:]]*PDP11_LISTEN=' "$ENV_FILE"; then
		sed -i -E "0,/^[#[:space:]]*PDP11_LISTEN=.*/s//PDP11_LISTEN=:$port/" "$ENV_FILE"
	else
		printf '\nPDP11_LISTEN=:%s\n' "$port" >>"$ENV_FILE"
	fi
	log "set PDP11_LISTEN=:$port in $ENV_FILE"
fi

# env_value KEY: the value of an active KEY= line of the settings file.
env_value() {
	sed -n -E "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "$ENV_FILE" | tail -n 1 |
		sed -E "s/[[:space:]]+\$//; s/^'(.*)'\$/\1/; s/^\"(.*)\"\$/\1/"
}

# ---- archives ---------------------------------------------------------------

prefetch_archives() {
	local fd=$work/fetch ids f b
	[ "$prefetch" != none ] || return 0
	log "prefetching archives ($prefetch) into $CACHEDIR"
	case $prefetch in
	default)
		ids=$("$BINDIR/pdp11d" check | awk '/ \(default\)$/ { print $1; exit }')
		[ -n "$ids" ] || { warn "cannot find the catalog's default system; nothing prefetched"; return 0; }
		;;
	all) ids= ;;
	esac
	mkdir -p "$fd"
	if [ -n "$archives" ]; then
		# Through the import directory of a scratch data directory: files of
		# any name, matched by size and sha256.
		mkdir -p "$fd/import"
		cp -R "$archives/." "$fd/import/"
		# shellcheck disable=SC2086
		"$BINDIR/pdp11d" fetch --data "$fd" --cache "$fd/cache" $ids || return 1
		for f in "$fd"/cache/*; do
			b=${f##*/}
			[ ${#b} -eq 64 ] || continue
			case $b in *[!0-9a-f]*) continue ;; esac
			mv -f "$f" "$CACHEDIR/$b"
		done
	else
		# In place: archives already in the cache are verified, not downloaded.
		# shellcheck disable=SC2086
		"$BINDIR/pdp11d" fetch --data "$fd" --cache "$CACHEDIR" $ids || return 1
	fi
	chmod 0644 "$CACHEDIR"/* 2>/dev/null || true
}

if ! prefetch_archives; then
	warn "could not prefetch the archives; pdp11d downloads them when a system is first powered on"
fi

# ---- service ----------------------------------------------------------------

# service_runs: whether pdp11d.service is still active a few seconds after
# its start. A start that fails (a port it may not bind, a data directory
# that another pdp11d uses, a bad setting) puts it into its restart loop.
service_runs() {
	local i
	for ((i = 0; i < 5; i++)); do
		sleep 1
		systemctl is-active --quiet pdp11d.service || return 1
	done
}

install -d -m 0755 "$(dirname "$UNIT")"
install -m 0644 "$here/deploy/pdp11d.service" "$UNIT"
running=0
failed=0
if use_systemd; then
	systemctl daemon-reload
	systemctl enable --quiet pdp11d.service
	if [ "$start" = 1 ]; then
		if systemctl is-active --quiet pdp11d.service; then
			log "restarting pdp11d.service (a running guest OS is shut down first)"
			systemctl restart pdp11d.service
		else
			log "starting pdp11d.service"
			systemctl start pdp11d.service
		fi
		if service_runs; then
			running=1
		else
			failed=1
			warn "pdp11d.service does not keep running"
		fi
	elif systemctl is-active --quiet pdp11d.service; then
		warn "pdp11d.service runs the old version until it is restarted (--no-start)"
	else
		log "enabled pdp11d.service; not started (--no-start)"
	fi
else
	[ "$systemd" = 0 ] || warn "systemd is not running here: installed the files only"
	if [ -n "$(pdp11d_pids)" ]; then
		warn "a pdp11d is running from $BINDIR: restart it to run the new version"
	fi
fi

# ---- summary ----------------------------------------------------------------

listen=$(env_value PDP11_LISTEN)
listen=${listen:-:8080}
lport=${listen##*:}
lhost=${listen%:*}
lhost=${lhost#[}
lhost=${lhost%]}
urls=()
case $lhost in
'' | 0.0.0.0 | ::)
	for ip in $(hostname -I 2>/dev/null || true); do
		case $ip in *:*) urls+=("http://[$ip]:$lport/") ;; *) urls+=("http://$ip:$lport/") ;; esac
	done
	[ ${#urls[@]} -gt 0 ] || urls+=("http://$(hostname 2>/dev/null || echo localhost):$lport/")
	;;
*:*) urls+=("http://[$lhost]:$lport/") ;;
*) urls+=("http://$lhost:$lport/") ;;
esac

echo
if [ "$failed" = 1 ]; then
	echo "The PDP-11/70 appliance is installed, but pdp11d.service fails to start. See"
	echo "    systemctl status pdp11d"
	echo "    journalctl -u pdp11d -n 50"
	echo
else
	echo "The PDP-11/70 appliance is installed."
fi
if [ "$running" = 1 ]; then
	echo "Open the web UI (the first start installs and boots the default system):"
else
	echo "Once pdp11d runs, the web UI is at:"
fi
for u in "${urls[@]}"; do echo "    $u"; done
echo
if ! use_systemd; then
	echo "Start it (as root; it runs as the user $SVC_USER) with"
	echo "    $BINDIR/pdp11d-run"
	echo "and stop it with SIGTERM, allowing 90 s for the clean shutdown of the guest OS."
	echo
fi
if [ -z "$(env_value PDP11_PASSWORD)" ]; then
	echo "No password is set: anyone who can reach port $lport controls the machine."
	echo "Set PDP11_PASSWORD in $ENV_FILE, then restart pdp11d$(use_systemd && echo ' (systemctl restart pdp11d)')."
else
	echo "The UI asks for the password in $ENV_FILE (PDP11_PASSWORD, any user name)."
fi
[ "$new_env" = 0 ] || echo "Settings: $ENV_FILE (see README.md, \"Configuration\")."
if use_systemd; then echo "Logs: journalctl -u pdp11d"; fi
[ "$failed" = 0 ] || exit 1
