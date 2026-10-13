.PHONY: build test test-prepare test-unit test-int test-e2e lint test-integration check test-macos-supervision test-utm test-qualification

GO ?= go
OUTPUT ?= bin/haepari

build:
	GO="$(GO)" sh build/build.sh "$(OUTPUT)"

test:
	+$(MAKE) test-prepare
	+$(MAKE) test-unit
	+$(MAKE) test-int
	+$(MAKE) test-e2e

test-prepare:
	gofmt -w $$(find cmd internal -type f -name '*.go')
	+$(MAKE) lint
	$(GO) vet -tags=integration ./...
	$(GO) vet -tags=macos_supervision ./internal/platform
	$(GO) build ./...
	uv lock --check --offline
	python3 -m compileall -q tests/e2e tests/qualification
	python3 docs/tools/validate_docs.py

test-unit:
	$(GO) run github.com/onsi/ginkgo/v2/ginkgo --race --fail-on-empty --fail-on-pending --label-filter=unit ./cmd/haepari ./internal/client ./internal/managed ./internal/platform ./internal/runner ./internal/snapshot ./internal/state ./internal/utm
	+$(MAKE) test-qualification

test-qualification:
	uv run --locked --group dev python -m pytest -q tests/qualification

test-int:
	$(GO) run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --fail-on-pending --label-filter=integration --poll-progress-after=1m --poll-progress-interval=1m ./cmd/haepari ./internal/controller ./internal/managed ./internal/runner ./internal/store/sqlite ./internal/utm

test-e2e:
	+$(MAKE) build
	uv run --locked --group dev python -m pytest -q tests/e2e

lint:
	@test -z "$$(gofmt -l $$(find cmd internal -type f -name '*.go'))" || (echo 'Run gofmt on Go sources' >&2; exit 1)
	$(GO) vet ./...

test-integration:
	+$(MAKE) test-int

check:
	+$(MAKE) test

test-macos-supervision:
	@test "$$(uname -s)" = Darwin && test "$$(uname -m)" = arm64 || { echo 'macOS arm64 host required for supervision gate' >&2; exit 2; }
	$(GO) run github.com/onsi/ginkgo/v2/ginkgo --race --tags=macos_supervision --fail-on-empty --label-filter=macos-supervision ./internal/platform
	$(GO) run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --label-filter=integration ./internal/store/sqlite
	$(GO) run github.com/onsi/ginkgo/v2/ginkgo --race --fail-on-empty --label-filter=unit ./internal/state
	$(GO) run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --label-filter=integration --focus=OwnedBackgroundGroupIsStopped ./internal/controller

test-utm:
	@python3 tests/qualification/utm_campaign.py "$${HAEPARI_UTM_MODE:-run}" \
		--campaign-file "$${HAEPARI_UTM_CAMPAIGN:?Set the exact private campaign file}" \
		--authority-file "$${HAEPARI_UTM_AUTHORITY:?Set the approved authority file}" \
		--evidence-root "$${HAEPARI_UTM_EVIDENCE:?Set the private campaign evidence directory}"
