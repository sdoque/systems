# Makefile for cross-compiling Arrowhead systems to Raspberry Pi 4/5 (64-bit OS)

STAGING    := $(HOME)/go/src/github.com/sdoque/rpiExec
GOOS       := linux
GOARCH     := arm64

# Build metadata — override VERSION on the command line: make rpi VERSION=1.2.3
VERSION    ?= dev
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_HASH := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
PKG        := github.com/sdoque/mbaigo/components

SYSTEMS := beehive beekeeper busdriver clerk collector democrat \
           drafter ds18b20 emulator esr ethermostat filmer flattener kgrapher \
           leveler messenger meteorologue modeler modboss nurse orchestrator \
           parallax photographer recognizer revolutionary sapper sailor \
           telegrapher thermostat tracker uaclient weatherman

.PHONY: all ci release rpi test lint clean $(SYSTEMS)

# Default target: build everything
all: rpi

# Clean rebuild with version stamp: make release VERSION=1.2.3
release: clean rpi

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

# --- Housekeeping -------------------------------------------------------------

clean:
	rm -rf $(STAGING)
	@echo "Staging directory $(STAGING) removed"
