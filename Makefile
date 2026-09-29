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
	@test "$$(uname -s)" = Darwin && test "$$(uname -m)" = arm64 || { echo 'macOS arm64 host required for supervision gate' >&2; exit 2; }
	$(GO) test -tags=macos_supervision -count=1 -v ./internal/platform
	$(GO) test -count=1 ./internal/store/sqlite ./internal/state
	$(GO) test -tags=integration -count=1 -run '^TestOwnedBackgroundGroupIsStopped$$' ./internal/controller

test-utm:
	@echo 'Real UTM qualification belongs to TASK-016' >&2
	@exit 2
