# Makefile: build, test, run and package the PDP-11/70 appliance.
#
#   make build          bin/pdp11 (patched Open SIMH, simh/build.sh) and bin/pdp11d
#   make test           go vet, gofmt check, go test, catalog check, boot-matrix
#                       harness and pdp11d-run unit tests, UI tests (when Node and
#                       web/dev/node_modules exist)
#   make test-simh      the SimH patch suite (quick mode) against bin/pdp11
#   make test-e2e       browser tests against the real stack (tests/e2e/README.md)
#   make run            pdp11d from bin/ with the UI read from web/ (development)
#   make docker         image $(IMAGE):$(TAG) for this machine
#   make docker-arm64   linux/arm64 image (buildx; no emulation needed)
#   make dist           dist/pdp11emu-VERSION-linux-ARCH.tar.gz: prebuilt binaries, their
#                       licence texts and install.sh
#   make dist-docker    the same, binaries built by the Dockerfile for DIST_PLATFORM
#   make dist-arm64     dist-docker for linux/arm64
#   make clean          remove bin/ and dist/ (distclean: also simh/src and simh/out)
#
# Variables (make VAR=value): BIN, SIMH_SRC, VERSION, SOURCE_URL, GO, GOTESTFLAGS,
# IMAGE, TAG, PREFETCH, DATA, LISTEN, SHM, PORT_BASE, DIST, DIST_PLATFORM,
# DIST_GO_IMAGE, ARM64_OUTPUT, SOURCE_DATE_EPOCH, PDP11_TEST_CACHE.

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := build

BIN         ?= bin
SIMH_SRC    ?= simh/src
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
# The image's org.opencontainers.image.source label: the URL of the git
# remote "origin" if it is an http(s) or scp-style address, without credentials.
SOURCE_URL  ?= $(shell git remote get-url origin 2>/dev/null | \
	sed -nE 's,^[^/@:]+@([^/:]+):,https://\1/,; s,^(https?://)[^/@]*@,\1,; /^https?:\/\//p')
GO          ?= go
GOTESTFLAGS ?=
IMAGE       ?= pdp11emu
TAG         ?= latest
PREFETCH    ?= default
DIST        ?= dist

# make run: a data directory, listener, panel file and console port of its
# own, so that it does not collide with an installed pdp11d on this machine
# (whose ":8080" also holds 127.0.0.1:8080).
DATA        ?= data
LISTEN      ?= 127.0.0.1:8081
SHM         ?= /dev/shm/pdp11d-dev-$(shell id -u)
PORT_BASE   ?= 23450

ARCH        := $(shell $(GO) env GOARCH 2>/dev/null || dpkg --print-architecture 2>/dev/null || uname -m)
LDFLAGS     := -s -w -X main.version=$(VERSION)
SIMH_DEPS   := simh/build.sh simh/SIMH_COMMIT $(wildcard simh/patches/*.patch) $(wildcard simh/panel/*.[ch])
DIST_FILES  := install.sh README.md LICENSE THIRD_PARTY.md deploy/pdp11d.service deploy/pdp11d.env deploy/pdp11d-run
ARM64_OUTPUT ?= -t $(IMAGE):$(TAG)-arm64 --load
DIST_PLATFORM ?= linux/arm64
DIST_GO_IMAGE ?=
# The mtime of every file in a dist tarball: the time of the last commit.
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)

# Archives for the tests that boot an operating system (README.md
# "Development"): a directory of catalog archives named by their sha256, as
# "pdp11d fetch --cache DIR" writes it; without it those tests are skipped.
PDP11_TEST_CACHE ?=
ifneq ($(PDP11_TEST_CACHE),)
export PDP11_TEST_CACHE
endif

.PHONY: build pdp11d test test-go test-catalog test-harness test-deploy test-ui test-simh test-e2e run \
	docker docker-arm64 dist dist-docker dist-arm64 clean distclean help

build: $(BIN)/pdp11 pdp11d

# SimH's makefile has no incremental build (about 20 s): rebuild only when
# the patches, the panel device or the pinned commit change. MAKEFLAGS is
# dropped so that variables given to this make (BIN=...) do not reach SimH's
# makefile, which has variables of the same names.
$(BIN)/pdp11: $(SIMH_DEPS)
	env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL simh/build.sh --src $(SIMH_SRC) --out $(BIN)

pdp11d:
	mkdir -p $(BIN)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/pdp11d ./server/cmd/pdp11d

test: test-go test-catalog test-harness test-deploy test-ui

test-go:
	$(GO) vet ./...
	@unformatted=$$(gofmt -l embed.go server); \
	if [ -n "$$unformatted" ]; then echo "gofmt -l: $$unformatted"; exit 1; fi
	$(GO) test $(GOTESTFLAGS) ./...

test-catalog:
	$(GO) run ./server/cmd/catalogcheck systems

test-harness:
	python3 -I tests/systems/test_bootmatrix.py

test-deploy:
	python3 -I deploy/test_pdp11d_run.py

# UI tests against the mock server (headless Chromium via playwright-core:
# see web/dev/README.md; they are skipped when it is not available).
test-ui:
	@if command -v node >/dev/null 2>&1 && [ -d web/dev/node_modules/ws ]; then \
		cd web/dev && npm test; \
	else \
		echo "test-ui: skipped (needs Node 20+ and 'npm install' in web/dev)"; \
	fi

test-simh: $(BIN)/pdp11
	python3 -I simh/test/run_tests.py --bin $(BIN)/pdp11 --quick

# Needs Node 20+ and playwright-core (see tests/e2e/README.md); downloads
# 2.11BSD unless PDP11_CACHE holds its archive.
test-e2e: $(BIN)/pdp11
	cd tests/e2e && PDP11_SIMH=$(abspath $(BIN)/pdp11) npm test

run: build
	mkdir -p $(DATA)
	PDP11_WEB=web PDP11_SIMH=$(abspath $(BIN)/pdp11) PDP11_DATA=$(abspath $(DATA)) \
	PDP11_LISTEN=$(LISTEN) PDP11_SHM=$(SHM) PDP11_PORT_BASE=$(PORT_BASE) \
		$(BIN)/pdp11d serve

docker:
	docker build --build-arg PREFETCH=$(PREFETCH) --build-arg VERSION=$(VERSION) \
		--build-arg SOURCE_URL=$(SOURCE_URL) -t $(IMAGE):$(TAG) .

# The containerd image store (or a docker-container builder) loads foreign
# images; else use ARM64_OUTPUT='--output type=oci,dest=pdp11emu-arm64.tar'.
docker-arm64:
	docker buildx build --platform linux/arm64 --build-arg PREFETCH=$(PREFETCH) \
		--build-arg VERSION=$(VERSION) --build-arg SOURCE_URL=$(SOURCE_URL) $(ARM64_OUTPUT) .

# dist: a tarball for machines without a toolchain; install.sh in it installs
# the binaries from its bin/ and the licence texts from its licenses/. The
# simulator needs the glibc of the machine it was built on or a newer one:
# build on the oldest system you deploy to. The Dockerfile builds on Debian 13
# (the binaries need glibc 2.38: Debian 13, Ubuntu 24.04);
# DIST_GO_IMAGE=golang:1.27-bookworm makes Debian 12 binaries. The tarball does
# not depend on the builder's account or umask: owner 0:0, no group or other
# write permission, sorted, every mtime SOURCE_DATE_EPOCH, gzip without a
# timestamp. package: $(1) name, $(2) directory of the binaries, $(3) of the
# licence texts.
define package
	rm -rf $(DIST)/$(1)
	install -d -m 0755 $(DIST)/$(1) $(DIST)/$(1)/bin $(DIST)/$(1)/deploy
	install -m 0755 $(2)/pdp11 $(2)/pdp11d $(DIST)/$(1)/bin/
	for f in $(DIST_FILES); do install -m $$([ -x $$f ] && echo 0755 || echo 0644) $$f $(DIST)/$(1)/$$f; done
	cp -R $(3) $(DIST)/$(1)/licenses
	chmod -R u=rwX,go=rX $(DIST)/$(1)/licenses
	tar -C $(DIST) --sort=name --owner=0 --group=0 --numeric-owner --mode=go-w \
		--mtime=@$(SOURCE_DATE_EPOCH) -cf - $(1) | gzip -9n >$(DIST)/$(1).tar.gz
	rm -rf $(DIST)/$(1)
	@echo "wrote $(DIST)/$(1).tar.gz"
endef

dist: build
	rm -rf $(DIST)/.licenses
	GO=$(GO) deploy/collect-licenses.sh --simh-src $(SIMH_SRC) $(DIST)/.licenses
	$(call package,pdp11emu-$(VERSION)-linux-$(ARCH),$(BIN),$(DIST)/.licenses)
	rm -rf $(DIST)/.licenses

dist-docker:
	rm -rf $(DIST)/.bin
	docker buildx build --platform $(DIST_PLATFORM) --build-arg VERSION=$(VERSION) \
		$(if $(DIST_GO_IMAGE),--build-arg GO_IMAGE=$(DIST_GO_IMAGE)) \
		--target binaries --output type=local,dest=$(DIST)/.bin .
	$(call package,pdp11emu-$(VERSION)-linux-$(notdir $(DIST_PLATFORM)),$(DIST)/.bin,$(DIST)/.bin/licenses)
	rm -rf $(DIST)/.bin

dist-arm64:
	$(MAKE) dist-docker DIST_PLATFORM=linux/arm64

clean:
	rm -rf $(BIN) $(DIST)

distclean: clean
	rm -rf simh/src simh/out

help:
	@sed -n '2,/^$$/{s/^# \{0,1\}//;p}' Makefile
