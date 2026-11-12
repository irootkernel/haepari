.PHONY: build test test-prepare test-unit test-int test-e2e lint test-integration check test-macos-supervision test-utm test-qualification release

TC = "$(CURDIR)/tools/toolchain"
ifneq ($(origin GO),undefined)
export GO
endif
.DEFAULT_GOAL := build
OUTPUT ?= bin/haepari
RELEASE_VERSION ?= 0.1.1
RELEASE_OUTPUT ?= dist

build:
	sh build/build.sh "$(OUTPUT)"

release:
	sh build/release.sh "$(RELEASE_VERSION)" "$(RELEASE_OUTPUT)"

test:
	+$(MAKE) test-prepare
	+$(MAKE) test-unit
	+$(MAKE) test-int
	+$(MAKE) test-e2e

test-prepare:
	$(TC) check all
	+$(MAKE) deps
	+$(MAKE) fmt
	+$(MAKE) lint
	$(TC) go vet -tags=integration ./...
	$(TC) go vet -tags=macos_supervision ./internal/platform
	$(TC) go build ./...
	$(TC) uv lock --check --offline
	$(TC) python tools/check_syntax.py
	+$(MAKE) docs-update

test-unit:
	$(TC) go run github.com/onsi/ginkgo/v2/ginkgo --race --fail-on-empty --fail-on-pending --label-filter=unit ./cmd/haepari ./internal/client ./internal/managed ./internal/platform ./internal/runner ./internal/snapshot ./internal/state ./internal/utm ./internal/config
	+$(MAKE) test-qualification
	+$(MAKE) test-toolchain

test-qualification:
	$(TC) pytest -q tests/qualification

test-int:
	$(TC) go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --fail-on-pending --label-filter=integration --poll-progress-after=1m --poll-progress-interval=1m ./cmd/haepari ./internal/controller ./internal/managed ./internal/runner ./internal/store/sqlite ./internal/utm

test-e2e:
	+$(MAKE) build
	$(TC) pytest -q tests/e2e

lint:
	$(TC) python tools/check_format.py
	$(TC) go vet ./...

test-integration:
	+$(MAKE) test-int

check:
	+$(MAKE) test

test-macos-supervision:
	@test "$$(uname -s)" = Darwin && test "$$(uname -m)" = arm64 || { echo 'macOS arm64 host required for supervision gate' >&2; exit 2; }
	$(TC) go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=macos_supervision --fail-on-empty --label-filter=macos-supervision ./internal/platform
	$(TC) go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --label-filter=integration ./internal/store/sqlite
	$(TC) go run github.com/onsi/ginkgo/v2/ginkgo --race --fail-on-empty --label-filter=unit ./internal/state
	$(TC) go run github.com/onsi/ginkgo/v2/ginkgo --race --tags=integration --fail-on-empty --label-filter=integration --focus=OwnedBackgroundGroupIsStopped ./internal/controller

test-utm:
	@$(TC) python tests/qualification/utm_campaign.py "$${HAEPARI_UTM_MODE:-run}" \
		--campaign-file "$${HAEPARI_UTM_CAMPAIGN:?Set the exact private campaign file}" \
		--authority-file "$${HAEPARI_UTM_AUTHORITY:?Set the approved authority file}" \
		--evidence-root "$${HAEPARI_UTM_EVIDENCE:?Set the private campaign evidence directory}"

.PHONY: help toolchain-install toolchain-check deps fmt docs-check docs-update test-toolchain
help:
	@echo 'toolchain-install  Explicitly install pinned Go, CPython and uv in the Haepari cache'
	@echo 'toolchain-check    Read-only version/path diagnosis'
	@echo 'deps               Prepare Go modules and the locked Python dev environment'
	@echo 'fmt                Explicitly format Go sources'
	@echo 'docs-check         Check generated toolchain snapshots and documentation'
	@echo 'docs-update        Generate toolchain snapshots and documentation, then check'
	@echo 'test-prepare       Prepare locked dependencies, format, build, generate and validate docs'
	@echo 'build release test test-unit test-int test-e2e lint check'

toolchain-install:
	$(TC) install

toolchain-check:
	$(TC) check

deps:
	$(TC) deps

fmt:
	$(TC) python tools/check_format.py --write

docs-check:
	$(TC) python tools/sync_toolchains.py --check
	$(TC) python docs/tools/validate_docs.py

docs-update:
	$(TC) python tools/sync_toolchains.py --write
	$(TC) python docs/tools/validate_docs.py --write
	+$(MAKE) docs-check

test-toolchain:
	$(TC) pytest -q tests/toolchain
