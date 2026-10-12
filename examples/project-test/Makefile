.PHONY: test test-vm test-vm-guest

HAEPARI ?= haepari
RESOURCE ?=
SUITE ?= regression
MODE ?= auto
RUN_DIR ?=

test:
	@test "$$(cat source/input.txt)" = ready

test-vm:
	@python3 host.py --haepari "$(HAEPARI)" --resource "$(RESOURCE)" --suite "$(SUITE)" --mode "$(MODE)" --run-dir "$(RUN_DIR)"

test-vm-guest:
	@cd source && /bin/sh ../tests/vm/run.sh
