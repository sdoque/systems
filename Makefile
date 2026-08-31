# Makefile for cross-compiling Arrowhead systems to Raspberry Pi 4/5 (64-bit OS)

STAGING    := $(HOME)/go/src/github.com/sdoque/rpiExec
GOOS       := linux
GOARCH     := arm64

# Build metadata — override VERSION on the command line: make rpi VERSION=1.2.3
VERSION    ?= dev
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_HASH := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
PKG        := github.com/sdoque/mbaigo/components

SYSTEMS := assessor authorizer beehive beekeeper busdriver ca clerk collector democrat \
           drafter ds18b20 ds18b20F emulator envoy esr ethermostat filmer flattener \
           hobbyist kgrapher leveler maitreD meteorologue modboss \
           modeler nurse orchestrator painter parallax photographer recognizer \
           revolutionary sailor sapper telegrapher thermostat tracker \
           uaclient weatherman

.PHONY: all ci release rpi win mac test lint clean whitelist $(SYSTEMS)

# The systems worth building for a host that is not a Raspberry Pi: a maitreD
# to attest on it, a registrar so its systems' default is right, and the
# systems that need no hardware. Override on the command line.
PORTABLE ?= maitreD esr thermostat painter envoy kgrapher modeler collector

# win builds them for a Windows machine, into the same staging tree as
# _win64.exe; mac for this Mac, as _mac64, for whatever this Mac's own
# architecture is — the maitreD needs cgo there, and cgo is off for a cross
# build, so a hard-coded arm64 would have failed to compile on an Intel Mac.
# Both regenerate the whitelist, because a binary that is built and not
# hashed is one the CA will refuse.
MACARCH := $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
win: $(foreach sys,$(PORTABLE),$(STAGING)/$(sys)/$(sys)_win64.exe)
	@$(MAKE) --no-print-directory whitelist
mac: $(foreach sys,$(PORTABLE),$(STAGING)/$(sys)/$(sys)_mac64)
	@$(MAKE) --no-print-directory whitelist

define build_portable
$(call build_for,$(1),windows,amd64,_win64.exe)
$(call build_for,$(1),darwin,$(MACARCH),_mac64)
endef
$(foreach sys,$(PORTABLE),$(eval $(call build_portable,$(sys))))

# Default target: build everything
all: rpi

# Clean rebuild with version stamp: make release VERSION=1.2.3
# Produces both the cross-compiled binaries and the matching whitelist.json
# that authorises exactly those binaries for certificate issuance.
release: clean rpi whitelist

# Full pipeline: tests and lint must pass before building
ci: lint test rpi

# Run tests in every system directory
test:
	@echo "=== Running tests ==="
	@for sys in $(SYSTEMS); do \
		echo "--- $$sys ---"; \
		(cd $$sys && go test .) || exit 1; \
	done
	@echo ""

# Run gofmt and go vet in every system directory
lint:
	@echo "=== Running lint ==="
	@for sys in $(SYSTEMS); do \
		echo "--- $$sys ---"; \
		(cd $$sys && test -z "$$(gofmt -l .)" || (echo "$$sys: code is not gofmt'ed" && exit 1)) || exit 1; \
		(cd $$sys && go vet .) || exit 1; \
	done
	@echo ""

# Build all systems and report when done
rpi: $(SYSTEMS)
	@echo ""
	@echo "All systems built — binaries are in $(STAGING)"

# --- Per-system targets -------------------------------------------------------
#
# The define/endef block is a reusable template.
# $(1) is replaced by the system name when the template is expanded.
# foreach loops over SYSTEMS, calling eval to turn each expansion into a rule.

# What every binary is stamped with, in one place: a flag added here reaches
# every platform, and a Windows maitreD cannot ship reporting Version=dev
# because the portable rule forgot it.
LDFLAGS = -X '$(PKG).AppName=$(1)' -X '$(PKG).Version=$(VERSION)' -X '$(PKG).BuildDate=$(BUILD_DATE)' -X '$(PKG).BuildHash=$(BUILD_HASH)'

# build_for: $(1) system, $(2) GOOS, $(3) GOARCH, $(4) suffix. One rule shape
# for every platform. The source list is found once per system, below, rather
# than once per rule per make invocation.
define build_for
$(STAGING)/$(1)/$(1)$(4): $$($(1)_SRC)
	@mkdir -p $(STAGING)/$(1)
	cd $(1) && GOOS=$(2) GOARCH=$(3) go build -ldflags "$(LDFLAGS)" -o $(STAGING)/$(1)/$(1)$(4)
endef

define build_system
$(1)_SRC := $(shell find $(1) -name '*.go' 2>/dev/null)
$(1): $(STAGING)/$(1)/$(1)_rpi64 $(if $(wildcard $(1)/README.md),$(STAGING)/$(1)/README.md)
	@echo "$(1) done"
$(call build_for,$(1),$(GOOS),$(GOARCH),_rpi64)

$(STAGING)/$(1)/README.md: $(1)/README.md
	@mkdir -p $(STAGING)/$(1)
	cp $(1)/README.md $(STAGING)/$(1)/
endef

$(foreach sys,$(SYSTEMS),$(eval $(call build_system,$(sys))))

# --- Whitelist generation -----------------------------------------------------
#
# A release of mbaigo systems must be paired with a whitelist that authorises
# exactly the binaries in that release. The security/ca Certificate Authority
# reads `whitelist.json` (a flat JSON array of SHA-256 hex strings) at runtime
# and serves it to maitreDs on every host; the maitreDs deny attestation for
# any process whose hash is not on that list.
#
# This section walks the just-built binaries in $(STAGING) and writes both
# files into $(STAGING)/ca/, alongside the CA binary they belong to:
#
#   $(STAGING)/ca/whitelist.json          — flat array of hashes; the wire
#                                            format the CA reads at runtime.
#   $(STAGING)/ca/whitelist-manifest.txt  — annotated `system → hash` map
#                                            with VERSION and BUILD_DATE,
#                                            for human review and audit.
#
# Co-locating with the CA binary lets a single `rsync $(STAGING)/ca/` deploy
# both the executable and the authorisation file as one atomic operation.
#
# Deployment: rsync the CA's directory to its host, e.g.
#     rsync -av $(STAGING)/ca/ ca-host:/path/to/ca/
# Every maitreD picks up the new list on its next sync (≤5 min by default).
#
# `release` depends on `whitelist`, so a single `make release VERSION=1.2.3`
# produces binaries and the matching authorisation file in one shot.
#
# Note: uses `shasum -a 256`, which is present on macOS and on most Linux
# distros. If your build host has only `sha256sum`, swap it in below.

whitelist: $(STAGING)/ca/whitelist.json $(STAGING)/ca/whitelist-manifest.txt

# Flat JSON array — the wire format expected by the CA's loadWhitelist().
# Depends on every staged binary, so editing any system's source and
# re-running `make rpi` causes the whitelist to regenerate automatically.
# Every staged binary of every platform: a Windows or macOS build placed
# beside the Pi ones is part of the same release and attested by the same CA.
#
# STAGED_BINS is both the prerequisite list and what is hashed, so the two
# cannot disagree. They did: the rule depended on the Pi binaries only, so a
# rebuilt Mac or Windows binary left the whitelist "up to date" and the CA
# serving a hash of the old one — and the first host to notice would have been
# the Windows machine, reporting "not in whitelist" for a reason the deployment
# document attributes to the firewall.
#
# Named from SYSTEMS and PORTABLE, not globbed from the directory: the
# whitelist is exactly this release's binaries, and a system dropped from the
# list — or something copied into staging by hand — does not stay attestable
# because its file is still lying there.
STAGED_BINS = $(wildcard $(foreach sys,$(SYSTEMS),$(STAGING)/$(sys)/$(sys)_rpi64) \
                         $(foreach sys,$(PORTABLE),$(STAGING)/$(sys)/$(sys)_win64.exe $(STAGING)/$(sys)/$(sys)_mac64))

$(STAGING)/ca/whitelist.json: $(foreach sys,$(SYSTEMS),$(STAGING)/$(sys)/$(sys)_rpi64) $(STAGED_BINS)
	@mkdir -p $(STAGING)/ca
	@printf '[\n' > $@
	@first=1; for bin in $(STAGED_BINS); do \
		hash=$$(shasum -a 256 $$bin | cut -d' ' -f1); \
		if [ $$first -eq 1 ]; then first=0; else printf ',\n' >> $@; fi; \
		printf '  "%s"' "$$hash" >> $@; \
	done
	@printf '\n]\n' >> $@
	@echo "Wrote $@"

# Human-readable manifest — never read by code, always read by people.
# Use this to answer "what binary is hash e3b0c44…?" during ops review.
$(STAGING)/ca/whitelist-manifest.txt: $(foreach sys,$(SYSTEMS),$(STAGING)/$(sys)/$(sys)_rpi64) $(STAGED_BINS)
	@mkdir -p $(STAGING)/ca
	@printf '# mbaigo whitelist manifest\n# VERSION=%s BUILD_DATE=%s\n# every staged binary, every platform\n\n' "$(VERSION)" "$(BUILD_DATE)" > $@
	@for bin in $(STAGED_BINS); do \
		hash=$$(shasum -a 256 $$bin | cut -d' ' -f1); \
		printf '%-40s %s\n' "$$(basename $$bin)" "$$hash" >> $@; \
	done
	@echo "Wrote $@"

# --- Housekeeping -------------------------------------------------------------

clean:
	rm -rf $(STAGING)
	@echo "Staging directory $(STAGING) removed"
