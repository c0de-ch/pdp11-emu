# syntax=docker/dockerfile:1.27
#
# PDP-11/70 appliance image: the patched Open SIMH simulator, pdp11d (web UI,
# front panel, console) and the archive of the default system, so that an
# empty volume boots 2.11BSD without a download (docs/DESIGN.md §13).
#
#   docker build -t pdp11emu:latest .
#   docker build --build-arg PREFETCH=all -t pdp11emu:latest .
#   docker buildx build --platform linux/arm64 -t pdp11emu:arm64 --load .
#   docker buildx build --platform linux/arm64 --target binaries --output type=local,dest=out .
#
# Build arguments:
#   PREFETCH    archives baked into /opt/pdp11/cache: "default" (the catalog's
#               default system, 2.11BSD, 24 MB), "all" (every redistributable
#               system, 79 MB), "none", or a list of system ids separated by
#               spaces or commas. Hobbyist-licensed systems (RT-11, RSX, RSTS)
#               are never baked in: the user accepts their licence and
#               installs them from the UI.
#   VERSION     version string of pdp11d (default: the code's 0.1.0-dev).
#   SOURCE_URL  value of the org.opencontainers.image.source label.
#   GO_IMAGE, RUNTIME_IMAGE
#               the build and runtime base images (golang:1.27-trixie,
#               debian:trixie-slim): tags on purpose, so that a rebuild picks
#               up their security updates. A reproducible build pins their
#               digests, e.g. --build-arg
#               RUNTIME_IMAGE=debian:trixie-slim@sha256:<digest> (shown by
#               "docker buildx imagetools inspect debian:trixie-slim"), and
#               the frontend of the syntax line with --build-arg
#               BUILDKIT_SYNTAX=docker/dockerfile:1.27@sha256:<digest>.
#
# Optional build context "archives": a directory of archives to use instead of
# downloading them, matched by size and sha256 whatever their names (for
# example a cache written by "pdp11d fetch --cache DIR"):
#   docker build --build-context archives=/path/to/dir -t pdp11emu:latest .
#
# Everything is compiled on the build platform (a cross C compiler for SimH,
# GOARCH for pdp11d) and the runtime stage has no RUN instruction, so an arm64
# image builds on an amd64 host without QEMU or binfmt emulation; the same
# Dockerfile also builds natively on a Raspberry Pi.

ARG GO_IMAGE=golang:1.27-trixie
ARG RUNTIME_IMAGE=debian:trixie-slim

# Archives for the prefetch: empty unless --build-context archives=DIR.
FROM scratch AS archives

# ---- build tools (build platform) -------------------------------------------------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS toolchain
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        build-essential git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src

# ---- SimH for the target platform ---------------------------------------------------
FROM toolchain AS simh
ARG TARGETARCH
ARG BUILDARCH
RUN set -eu; \
    [ "$TARGETARCH" = "$BUILDARCH" ] && exit 0; \
    case $TARGETARCH in \
    arm64) pkgs="gcc-aarch64-linux-gnu libc6-dev-arm64-cross" ;; \
    amd64) pkgs="gcc-x86-64-linux-gnu libc6-dev-amd64-cross" ;; \
    *) echo "unsupported target architecture $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends $pkgs; \
    rm -rf /var/lib/apt/lists/*
COPY simh/SIMH_COMMIT simh/build.sh simh/
COPY simh/patches/ simh/patches/
COPY simh/panel/ simh/panel/
# The SimH clone lives in a cache mount, so a rebuild fetches nothing from
# GitHub (build.sh resets and cleans the tree every time). A cross build skips
# the smoke check (the binary cannot run here) and SimH's BuildROMs helper
# (DONT_USE_ROMS=1), which would have to run on the build host; the PDP-11
# simulator needs no ROM it generates.
RUN --mount=type=cache,id=pdp11emu-simh-src,target=/cache/simh-src,sharing=locked \
    set -eu; \
    if [ "$TARGETARCH" = "$BUILDARCH" ]; then \
        simh/build.sh --src /cache/simh-src --out /out; \
    else \
        case $TARGETARCH in arm64) cc=aarch64-linux-gnu-gcc ;; amd64) cc=x86_64-linux-gnu-gcc ;; esac; \
        CC=$cc DONT_USE_ROMS=1 simh/build.sh --src /cache/simh-src --out /out --no-smoke; \
    fi; \
    mkdir -p /out/simh-licenses/slirp; \
    cp /cache/simh-src/LICENSE.txt /out/simh-licenses/; \
    cp /cache/simh-src/slirp/COPYRIGHT /out/simh-licenses/slirp/

# ---- Go sources ---------------------------------------------------------------------
FROM toolchain AS gosrc
COPY go.mod go.sum ./
RUN --mount=type=cache,id=pdp11emu-gomod,target=/go/pkg/mod \
    go mod download
COPY embed.go ./
COPY server/ server/
COPY systems/ systems/
COPY web/index.html web/
COPY web/assets/ web/assets/

# pdp11d for the build platform: runs "pdp11d fetch" below.
FROM gosrc AS pdp11d-native
RUN --mount=type=cache,id=pdp11emu-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=pdp11emu-gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/pdp11d ./server/cmd/pdp11d

# pdp11d for the target platform.
FROM gosrc AS pdp11d
ARG TARGETARCH
ARG VERSION
RUN --mount=type=cache,id=pdp11emu-gomod,target=/go/pkg/mod \
    --mount=type=cache,id=pdp11emu-gobuild,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath \
        -ldflags "-s -w${VERSION:+ -X main.version=$VERSION}" \
        -o /out/pdp11d ./server/cmd/pdp11d

# Licence texts shipped in /opt/pdp11/share/doc (THIRD_PARTY.md lists them):
# deploy/collect-licenses.sh, as for "make dist" and install.sh (the SimH
# files come from the simh stage, whose tree lives in a cache mount).
FROM gosrc AS licenses
COPY --chmod=0644 LICENSE THIRD_PARTY.md /out/doc/
COPY deploy/collect-licenses.sh deploy/
COPY --from=simh /out/simh-licenses/ /tmp/simh-licenses/
RUN --mount=type=cache,id=pdp11emu-gomod,target=/go/pkg/mod \
    deploy/collect-licenses.sh --simh-src /tmp/simh-licenses /out/doc/licenses

# ---- archive cache ------------------------------------------------------------------
# Independent of the target platform: a multi-platform build fetches once.
# "pdp11d fetch" verifies the size and sha256 of every archive, and any
# failure fails the build. Files of the "archives" context are offered
# through the import directory of a scratch data directory.
FROM toolchain AS cache
COPY --from=pdp11d-native /out/pdp11d /usr/local/bin/pdp11d
ARG PREFETCH=default
RUN --mount=type=bind,from=archives,target=/archives \
    set -eu; \
    mkdir -p /opt/pdp11/cache /tmp/fetch/import; \
    case $PREFETCH in \
    none|'') echo "PREFETCH=none: no archives in the image"; exit 0 ;; \
    all) ids= ;; \
    default) ids=$(pdp11d check | awk '/ \(default\)$/ { print $1; exit }'); \
             [ -n "$ids" ] || { echo "cannot find the catalog's default system" >&2; exit 1; } ;; \
    *) ids=$(echo "$PREFETCH" | tr ',' ' ') ;; \
    esac; \
    cp -R /archives/. /tmp/fetch/import/; \
    pdp11d fetch --data /tmp/fetch --cache /tmp/fetch/cache $ids; \
    find /tmp/fetch/cache -maxdepth 1 -type f -regextype posix-extended -regex '.*/[0-9a-f]{64}' \
        -exec mv -t /opt/pdp11/cache {} +; \
    rm -rf /tmp/fetch; \
    ls -l /opt/pdp11/cache

# ---- runtime user and data directory ------------------------------------------------
# /etc/passwd and /etc/group of the runtime image with the user pdp11 added
# (made on the build platform: plain text, the same on every architecture).
FROM --platform=$BUILDPLATFORM ${RUNTIME_IMAGE} AS users
RUN groupadd --gid 10001 pdp11 \
 && useradd --uid 10001 --gid pdp11 --home-dir /data --no-create-home \
        --shell /usr/sbin/nologin --no-log-init pdp11 \
 && mkdir /data

# ---- binaries and licences: --target binaries --output type=local,dest=DIR ----------
FROM scratch AS binaries
COPY --from=simh /out/pdp11 /pdp11
COPY --from=pdp11d /out/pdp11d /pdp11d
COPY --from=licenses /out/doc/licenses/ /licenses/

# ---- runtime (no RUN instructions) --------------------------------------------------
FROM ${RUNTIME_IMAGE}
ARG VERSION
ARG SOURCE_URL=
LABEL org.opencontainers.image.title="pdp11emu" \
      org.opencontainers.image.description="PDP-11/70 emulator appliance: Open SIMH with an operable front panel replica and a web console" \
      org.opencontainers.image.licenses="MIT AND X11 AND BSD-2-Clause AND BSD-3-Clause AND ISC AND OFL-1.1 AND Unlicense AND MPL-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="${SOURCE_URL}"
COPY --from=users /etc/passwd /etc/group /etc/
COPY --from=toolchain /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=simh /out/pdp11 /opt/pdp11/bin/pdp11
COPY --from=pdp11d /out/pdp11d /opt/pdp11/bin/pdp11d
COPY --from=licenses /out/doc/ /opt/pdp11/share/doc/
# The CA bundle's licence (MPL-2.0): the copyright file of its Debian package.
COPY --from=toolchain /usr/share/doc/ca-certificates/copyright /opt/pdp11/share/doc/licenses/ca-certificates.txt
COPY --from=cache /opt/pdp11/cache /opt/pdp11/cache
COPY --from=users --chown=10001:10001 /data /data
USER 10001:10001
# PDP11_SHUTDOWN_ON_STOP: "docker stop" first shuts the guest OS down
# cleanly, which needs a stop timeout of at least 75 s (docker stop -t 90,
# docker run --stop-timeout 90, compose stop_grace_period: 90s).
ENV PDP11_DATA=/data \
    PDP11_SHUTDOWN_ON_STOP=1
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --start-interval=2s --retries=3 \
    CMD ["/opt/pdp11/bin/pdp11d", "health"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/opt/pdp11/bin/pdp11d"]
CMD ["serve"]
