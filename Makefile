# Makefile for cross-compiling Arrowhead systems to Raspberry Pi 4/5 (64-bit OS)

STAGING    := $(HOME)/go/src/github.com/sdoque/rpiExec
GOOS       := linux
GOARCH     := arm64

# Build metadata — override VERSION on the command line: make rpi VERSION=1.2.3
VERSION    ?= dev
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_HASH := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
PKG        := github.com/sdoque/mbaigo/components

SYSTEMS := beehive beekeeper busdriver ca clerk collector democrat \
           drafter ds18b20 emulator esr ethermostat filmer flattener kgrapher \
           leveler maitreD messenger meteorologue modeler modboss nurse \
           orchestrator parallax photographer recognizer revolutionary sapper \
           sailor telegrapher thermostat tracker uaclient weatherman

.PHONY: all ci release rpi test lint clean whitelist $(SYSTEMS)

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

define build_system
$(1): $(STAGING)/$(1)/$(1)_rpi64 $(if $(wildcard $(1)/README.md),$(STAGING)/$(1)/README.md)
	@echo "$(1) done"

$(STAGING)/$(1)/$(1)_rpi64: $(shell find $(1) -name '*.go' 2>/dev/null)
	@mkdir -p $(STAGING)/$(1)
	cd $(1) && GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		-ldflags "-X '$(PKG).AppName=$(1)' \
		          -X '$(PKG).Version=$(VERSION)' \
		          -X '$(PKG).BuildDate=$(BUILD_DATE)' \
		          -X '$(PKG).BuildHash=$(BUILD_HASH)'" \
		-o $(STAGING)/$(1)/$(1)_rpi64

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
# This section walks the just-built binaries in $(STAGING) and writes:
#
#   whitelist.json          — flat array of hashes; the wire format the CA reads.
#   whitelist-manifest.txt  — annotated `system → hash` mapping with VERSION
#                              and BUILD_DATE, for human review and audit.
#
# Deployment: copy whitelist.json to the CA host's working directory, e.g.
#     scp $(STAGING)/whitelist.json ca-host:/path/to/ca/whitelist.json
# Every maitreD picks up the new list on its next sync (≤5 min by default).
#
# `release` depends on `whitelist`, so a single `make release VERSION=1.2.3`
# produces binaries and the matching authorisation file in one shot.
#
# Note: uses `shasum -a 256`, which is present on macOS and on most Linux
# distros. If your build host has only `sha256sum`, swap it in below.

whitelist: $(STAGING)/whitelist.json $(STAGING)/whitelist-manifest.txt

# Flat JSON array — the wire format expected by the CA's loadWhitelist().
# Depends on every staged binary, so editing any system's source and
# re-running `make rpi` causes the whitelist to regenerate automatically.
$(STAGING)/whitelist.json: $(foreach sys,$(SYSTEMS),$(STAGING)/$(sys)/$(sys)_rpi64)
	@printf '[\n' > $@
	@first=1; for sys in $(SYSTEMS); do \
		bin=$(STAGING)/$$sys/$${sys}_rpi64; \
		hash=$$(shasum -a 256 $$bin | cut -d' ' -f1); \
		if [ $$first -eq 1 ]; then first=0; else printf ',\n' >> $@; fi; \
		printf '  "%s"' "$$hash" >> $@; \
	done
	@printf '\n]\n' >> $@
	@echo "Wrote $@"

# Human-readable manifest — never read by code, always read by people.
# Use this to answer "what binary is hash e3b0c44…?" during ops review.
$(STAGING)/whitelist-manifest.txt: $(foreach sys,$(SYSTEMS),$(STAGING)/$(sys)/$(sys)_rpi64)
	@printf 'Whitelist manifest — VERSION=%s built %s\n\n' \
		"$(VERSION)" "$(BUILD_DATE)" > $@
	@for sys in $(SYSTEMS); do \
		bin=$(STAGING)/$$sys/$${sys}_rpi64; \
		hash=$$(shasum -a 256 $$bin | cut -d' ' -f1); \
		printf '%-20s  %s\n' "$$sys" "$$hash" >> $@; \
	done
	@echo "Wrote $@"

# --- Housekeeping -------------------------------------------------------------

clean:
	rm -rf $(STAGING)
	@echo "Staging directory $(STAGING) removed"
