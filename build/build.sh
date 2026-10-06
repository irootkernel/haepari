#!/bin/sh
set -eu

# Run from the repository root, just like the Makefile's other build commands.
output=${1:-bin/haepari}
mkdir -p "$(dirname "$output")"
if [ "$(uname -s)" = Darwin ]; then
    plist="$(pwd)/build/macos/Info.plist"
    # Go parses ldflags, then the external linker parses extldflags. Keep the
    # section argument quoted through both layers, including checkout spaces.
    CGO_ENABLED=1 "${GO:-go}" build -ldflags "-linkmode=external \"-extldflags='-Wl,-sectcreate,__TEXT,__info_plist,$plist'\"" -o "$output" ./cmd/haepari
    /usr/bin/codesign --force --sign - --identifier io.rootkernel.haepari "$output"
    /usr/bin/codesign --verify --strict "$output"
else
    "${GO:-go}" build -o "$output" ./cmd/haepari
fi
