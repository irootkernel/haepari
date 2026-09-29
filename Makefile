.PHONY: build test lint test-integration check test-macos-supervision test-utm

GO ?= go

build:
	@mkdir -p bin
	$(GO) build -o bin/haepari ./cmd/haepari

test:
	$(GO) test ./...

lint:
	@test -z "$$(gofmt -l $$(find cmd internal -name '*.go'))" || (echo 'Run gofmt on Go sources' >&2; exit 1)
	$(GO) vet ./...

test-integration:
	$(GO) test -tags=integration ./...

check: build test lint test-integration
	python3 docs/tools/validate_docs.py

test-macos-supervision:
	@echo 'TASK-004 macOS supervision gate is not yet implemented' >&2
	@exit 2

test-utm:
	@echo 'Real UTM qualification belongs to TASK-016' >&2
	@exit 2
