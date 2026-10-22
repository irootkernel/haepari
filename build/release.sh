#!/bin/sh
set -eu

# Assemble the unpublished Haepari release candidate package.
# Usage (from the repository root): sh build/release.sh [version] [output-dir]
# Produces <output-dir>/haepari-<version>-darwin-arm64.tar.gz plus a .sha256
# sidecar that binds the whole archive. SHA256SUMS inside the archive lists
# every file except itself. Existing outputs are never replaced destructively:
# the run refuses when the target archive already exists.
version=${1:-0.1.1}
output=${2:-dist}
case "$version" in
    ''|*[!0-9A-Za-z.-]*)
        echo "invalid version: $version" >&2
        exit 2
        ;;
esac
if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then
    echo 'release packaging requires darwin/arm64' >&2
    exit 2
fi
for required in README.md packaging/examples/resource-local.json \
    packaging/examples/resource-lock.json packaging/examples/job-local-auto.json \
    packaging/examples/job-local-manual.json skill/use-haepari/SKILL.md \
    LICENSE THIRD_PARTY_NOTICES.txt build/build.sh; do
    if [ ! -e "$required" ]; then
        echo "missing package input: $required (run from the repository root)" >&2
        exit 2
    fi
done

mkdir -p "$output"
output_abs=$(cd "$output" && pwd)
name="haepari-$version-darwin-arm64"
archive="$output_abs/$name.tar.gz"
if [ -e "$archive" ] || [ -e "$archive.sha256" ]; then
    echo "refusing to overwrite $archive; use a fresh output directory" >&2
    exit 2
fi

# Packaging scratch honors the caller TMPDIR (for task scratch volumes); only
# short controller fixture roots need the canonical /tmp pattern.
staging=$(mktemp -d "${TMPDIR:-/tmp}/haepari-release-$version.XXXXXX")
trap 'rm -rf "$staging"' EXIT
tree="$staging/$name"
mkdir -p "$tree/skill" "$tree/examples"

GO="${GO:-go}" sh build/build.sh "$tree/haepari" "$version"
cp README.md "$tree/README.md"
cp LICENSE "$tree/LICENSE"
cp THIRD_PARTY_NOTICES.txt "$tree/THIRD_PARTY_NOTICES.txt"
cp packaging/verify_tree.py "$tree/verify_tree.py"
cp packaging/examples/*.json "$tree/examples/"
cp -R skill/use-haepari "$tree/skill/use-haepari"

# SHA256SUMS covers every file in the tree except itself, in sorted order.
(
    cd "$tree"
    find . -type f ! -path ./SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort |
    while IFS= read -r entry; do
        shasum -a 256 "$entry"
    done > SHA256SUMS
)

(
    cd "$staging"
    LC_ALL=C find "$name" -type f | LC_ALL=C sort | COPYFILE_DISABLE=1 tar -czf "$archive" -T -
)

# The sidecar is basename-relative so `shasum -a 256 -c` keeps working after
# the archive and sidecar are moved together.
(
    cd "$output_abs"
    shasum -a 256 "$(basename "$archive")" > "$archive.sha256"
)
binary_sha=$(shasum -a 256 "$tree/haepari" | cut -d' ' -f1)
echo "archive: $archive"
echo "archive sha256: $(cut -d' ' -f1 "$archive.sha256")  $(basename "$archive")"
echo "binary sha256: $binary_sha"
echo "version stamped: $version"
