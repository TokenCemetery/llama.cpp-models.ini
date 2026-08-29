# Every command this repo needs. Run `make` on its own to see the list.
#
# The Go module lives in src/, so each target uses `go -C src`. You never need
# to cd anywhere: run make from the repository root.

GO ?= go
SRC := src

# Used by `make measure` to limit the run to one model:
#   make measure MODEL=qwen3-8b
MODEL ?=

.DEFAULT_GOAL := help
.PHONY: help build validate test check measure notes fmt vet clean

help: ## Show this help
	@echo "usage: make <target>"
	@echo
	@grep -E '^[a-z-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'
	@echo
	@echo "variables: MODEL=$(MODEL)  (limits 'make measure' to one model)"

build: ## Regenerate dist/ and MODELS.md
	$(GO) -C $(SRC) run ./cmd/llamapreset build

validate: ## Check every config against the rules in AGENTS.md
	$(GO) -C $(SRC) run ./cmd/llamapreset validate

test: ## Run the Go tests
	$(GO) -C $(SRC) test ./...

check: test build validate ## Run everything CI runs. Do this before committing

measure: ## Measure missing VRAM numbers (network, no downloads). MODEL=<id> for one
	$(GO) -C $(SRC) run ./cmd/llamapreset measure --missing $(MODEL)

notes: ## Print the release notes to stdout
	@$(GO) -C $(SRC) run ./cmd/llamapreset build --notes

fmt: ## Format the Go code
	$(GO) -C $(SRC) fmt ./...

vet: ## Report suspicious Go constructs
	$(GO) -C $(SRC) vet ./...

clean: ## Delete generated presets (MODELS.md is committed, so it stays)
	rm -f dist/*.ini
	@rmdir dist 2>/dev/null || true
