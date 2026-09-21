# Keryx — common build and verification tasks.
#
# Layout: sdk/ (publisher SDK + pub CLI), relay/ (notification relay),
# demo-tool/ (SDK-backed demo site generator), app/ (PWA / Capacitor client).
#
# Run `make` (or `make help`) for the target list.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# --- toolchain (override on the command line, e.g. `make GO=go1.26.0`) --------
GO      ?= go
NPM     ?= npm
PYTHON  ?= python3

# --- paths --------------------------------------------------------------------
BIN            ?= bin
SDK_DIR        ?= sdk
RELAY_DIR      ?= relay
DEMO_TOOL_DIR  ?= demo-tool
EXAMPLES_DIR   ?= examples
APP_DIR        ?= app

# --- demo settings ------------------------------------------------------------
# One demo, outside this repository (keryx-demo/README.md): the publisher artifact
# at ../keryx-demo (published as https://keryx-demo.github.io) and its keystore at
# ../keryx-demo-keys. Only the maintainer holds the release keys; without them
# `make demo` mints a fresh, independent demo — a new trust anchor that must not
# be pushed over the published site (spec/repository.md §5).
DEMO_REPO       ?= ../keryx-demo
DEMO_KEYS_DIR   ?= ../keryx-demo-keys
DEMO_BASE       ?= https://keryx-demo.github.io
DEMO_PORT       ?= 8000
# SDK-generated artifact used by the app's SDK tests (examples/sdk-artifact).
DEMO_SDK_DIR      ?= .demo-sdk
DEMO_SDK_KEYS_DIR ?= $(CURDIR)/.demo-sdk-keys
DEMO_SDK_BASE     ?= http://localhost:8000

APP_DEPS_STAMP := $(APP_DIR)/node_modules/.installed

# --- meta ---------------------------------------------------------------------

.PHONY: help all build test verify clean distclean
help: ## List the available targets
	@printf 'Keryx build targets (override variables, e.g. `make demo DEMO_BASE=https://x`):\n\n'
	@grep -hE '^[a-zA-Z0-9_./-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@printf '\nGo toolchains are selected automatically from go.mod.\n'

all: build ## Build everything buildable locally

build: cli relay demo-tool examples-build app-build ## Build the CLI, relay, showcase examples, demo tool and the PWA

# --- SDK + CLI ----------------------------------------------------------------

.PHONY: sdk-build sdk-test sdk-vet cli
sdk-build: ## Build every SDK package
	cd $(SDK_DIR) && $(GO) build ./...

sdk-test: ## Run the SDK test suite
	cd $(SDK_DIR) && $(GO) test ./...

sdk-vet: ## Vet the SDK
	cd $(SDK_DIR) && $(GO) vet ./...

cli: ## Build the pub publisher CLI into bin/pub
	mkdir -p $(BIN)
	cd $(SDK_DIR) && $(GO) build -o $(CURDIR)/$(BIN)/pub ./cmd/pub

# --- showcase consumers (examples/) -------------------------------------------

.PHONY: examples-build
examples-build: ## Compile-check the showcase consumers in examples/
	cd $(EXAMPLES_DIR) && $(GO) build -o /dev/null ./...

# --- relay --------------------------------------------------------------------

.PHONY: relay relay-test relay-e2e
relay: ## Build the notification relay into bin/relay
	mkdir -p $(BIN)
	cd $(RELAY_DIR) && $(GO) build -o $(CURDIR)/$(BIN)/relay ./cmd/relay

relay-test: ## Run the relay test suite
	cd $(RELAY_DIR) && $(GO) test ./...

relay-e2e: $(DEMO_REPO)/join.txt ## Run the relay end-to-end test against the demo
	cd $(RELAY_DIR) && KERYX_DEMO_DIR=$(abspath $(DEMO_REPO)) KERYX_KEYSTORE=$(abspath $(DEMO_KEYS_DIR)) $(GO) test -run TestEndToEndDemoRepository ./internal/e2e/...

# --- demo publisher artifact --------------------------------------------------

.PHONY: demo demo-verify demo-sdk serve-demo demo-tool
demo: ## Regenerate the demo site (../keryx-demo; mints fresh keys if absent)
	@test -f $(DEMO_KEYS_DIR)/master.json || echo "note: no release keystore at $(DEMO_KEYS_DIR) — minting a fresh, independent demo (new trust anchor; do not push it over the published site)"
	cd $(DEMO_TOOL_DIR) && $(GO) run . -mode build -site $(abspath $(DEMO_REPO)) -keys $(abspath $(DEMO_KEYS_DIR)) -base $(DEMO_BASE)

demo-verify: $(DEMO_REPO)/join.txt ## Verify the generated demo site
	cd $(DEMO_TOOL_DIR) && $(GO) run . -mode verify -site $(abspath $(DEMO_REPO)) -keys $(abspath $(DEMO_KEYS_DIR)) -base $(DEMO_BASE)

demo-sdk: ## Generate a minimal artifact from examples/sdk-artifact (SDK consumer)
	mkdir -p $(BIN)
	cd $(EXAMPLES_DIR) && $(GO) build -o $(CURDIR)/$(BIN)/keryxdemo ./sdk-artifact
	$(BIN)/keryxdemo --out $(CURDIR)/$(DEMO_SDK_DIR) --keys $(DEMO_SDK_KEYS_DIR) --base $(DEMO_SDK_BASE)

serve-demo: ## Serve the demo site with CORS (default port 8000)
	$(PYTHON) tools/serve.py --port $(DEMO_PORT) $(DEMO_REPO)

demo-tool: ## Build the demo site generator into bin/demo-tool
	mkdir -p $(BIN)
	cd $(DEMO_TOOL_DIR) && $(GO) build -o $(CURDIR)/$(BIN)/demo-tool .

# --- PWA / Capacitor app ------------------------------------------------------

# A stamp so `npm install` runs only when the lockfile changes (the
# node_modules directory mtime is not a reliable prerequisite).
$(APP_DEPS_STAMP): $(APP_DIR)/package-lock.json
	cd $(APP_DIR) && $(NPM) install
	@touch $(APP_DEPS_STAMP)

.PHONY: app-install app-dev app-build app-build-pages app-test app-test-sdk app-icons apk
app-install: $(APP_DEPS_STAMP) ## Install the app dependencies (npm install)

app-dev: $(APP_DEPS_STAMP) ## Run the app dev server (Vite)
	cd $(APP_DIR) && $(NPM) run dev

app-build: $(APP_DEPS_STAMP) ## Build the PWA into app/dist
	cd $(APP_DIR) && $(NPM) run build

app-build-pages: $(APP_DEPS_STAMP) ## Build the PWA for a GitHub Pages project site
	cd $(APP_DIR) && VITE_BASE=/keryx/ $(NPM) run build

app-test: $(DEMO_REPO)/join.txt $(APP_DEPS_STAMP) ## Run the protocol tests against the generated demo
	cd $(APP_DIR) && KERYX_DEMO_DIR=$(abspath $(DEMO_REPO)) KERYX_KEYSTORE=$(abspath $(DEMO_KEYS_DIR)) $(NPM) test

app-test-sdk: demo-sdk $(APP_DEPS_STAMP) ## Run the protocol tests against an SDK-generated artifact
	cd $(APP_DIR) && KERYX_DEMO_DIR=$(CURDIR)/$(DEMO_SDK_DIR) KERYX_KEYSTORE=$(DEMO_SDK_KEYS_DIR) $(NPM) test

app-icons: $(APP_DEPS_STAMP) ## Regenerate the PWA icons
	cd $(APP_DIR) && $(NPM) run icons

apk: $(APP_DEPS_STAMP) ## Build the Android debug APK (needs the Android SDK)
	cd $(APP_DIR) && $(NPM) run cap:android

# Generate the demo on first use; `make demo` regenerates explicitly.
$(DEMO_REPO)/join.txt:
	@$(MAKE) --no-print-directory demo

# --- verification -------------------------------------------------------------

test: sdk-test relay-test relay-e2e app-test ## Run every test suite

verify: sdk-vet sdk-test relay-test relay-e2e demo-verify app-test app-test-sdk ## Full local verification

# --- cleanup ------------------------------------------------------------------

clean: ## Remove build outputs (bin/, app/dist)
	rm -rf $(BIN) $(APP_DIR)/dist

distclean: clean ## Also remove node_modules and the SDK test artifact
	rm -rf $(APP_DIR)/node_modules $(DEMO_SDK_DIR) $(DEMO_SDK_KEYS_DIR)
