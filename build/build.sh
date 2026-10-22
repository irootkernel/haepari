#!/bin/sh
set -eu

# Run from the repository root, just like the Makefile's other build commands.
# Optional second argument stamps internal/identity.Version (release packaging);
# without it the build keeps the source default from internal/identity.Version.
output=${1:-bin/haepari}
version=${2:-}
mkdir -p "$(dirname "$output")"
version_flags=""
if [ -n "$version" ]; then
    version_flags="-X github.com/irootkernel/haepari/internal/identity.Version=$version"
fi
if [ "$(uname -s)" = Darwin ]; then
    plist="$(pwd)/build/macos/Info.plist"
    # Go parses ldflags, then the external linker parses extldflags. Keep the
    # section argument quoted through both layers, including checkout spaces.
    CGO_ENABLED=1 "${GO:-go}" build -ldflags "$version_flags -linkmode=external \"-extldflags='-Wl,-sectcreate,__TEXT,__info_plist,$plist'\"" -o "$output" ./cmd/haepari
    /usr/bin/codesign --force --sign - --identifier io.rootkernel.haepari "$output"
    /usr/bin/codesign --verify --strict "$output"
else
    "${GO:-go}" build ${version_flags:+-ldflags "$version_flags"} -o "$output" ./cmd/haepari
fi
