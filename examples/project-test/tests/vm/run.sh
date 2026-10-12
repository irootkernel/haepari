#!/bin/sh
set -eu

refuse() { printf '%s\n' 'This entrypoint requires the managed runner Job/lease context.' >&2; exit 2; }
test -n "${HAEPARI_PARENT_CONTEXT:-}" || refuse
job=$(printf '%s' "$HAEPARI_PARENT_CONTEXT" | /usr/bin/plutil -extract 0.job_id raw -o - -) || refuse
lease=$(printf '%s' "$HAEPARI_PARENT_CONTEXT" | /usr/bin/plutil -extract 0.lease_generation raw -o - -) || refuse
nonce=$(printf '%s' "$HAEPARI_PARENT_CONTEXT" | /usr/bin/plutil -extract 0.nonce raw -o - -) || refuse
case "$job" in ''|*[!a-zA-Z0-9._-]*) refuse;; esac
case "$lease" in ''|*[!0-9]*) refuse;; esac
test "$lease" -gt 0 || refuse
case "$nonce" in ''|*[!0-9a-f]*) refuse;; esac
test "${#nonce}" -eq 64 || refuse

mkdir -p ../results
cat input.txt > ../results/out.txt
if test -n "${COUNT_PATH:-}"; then printf x >> "$COUNT_PATH"; fi
printf 'project input: '
cat input.txt
if test "$(cat input.txt)" != "${EXPECTED_INPUT:-ready}"; then
    printf 'expected %s input\n' "${EXPECTED_INPUT:-ready}" >&2
    exit 7
fi
