#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
version=${1:-$(cat "$root/VERSION")}

case "$version" in
    *[!0-9A-Za-z._-]*|'')
        echo "Invalid release version: $version" >&2
        exit 1
        ;;
esac

command -v zip >/dev/null 2>&1 || {
    echo "zip is required to package a release" >&2
    exit 1
}

release_dir="$root/build/release"
temporary_root=${TMPDIR:-/tmp}
stage="$temporary_root/PocketStream-release-$version-$$"
archive="$release_dir/PocketStream-OnionOS-$version.zip"

case "$stage" in
    "$temporary_root"/PocketStream-release-*) ;;
    *) echo "Unsafe staging path" >&2; exit 1 ;;
esac

cleanup() {
    rm -rf -- "$stage"
}
trap cleanup 0 1 2 15

mkdir -p "$stage/App" "$release_dir"
cp -R "$root/dist/App/PocketStream" "$stage/App/PocketStream"

# Never package runtime state or development-only diagnostics.
find "$stage/App/PocketStream" -type f \( \
    -name '*.log' -o -name '*.log.1' -o -name 'search-history.txt' -o \
    -name '.privacy-log-format-v1' -o -name 'zapret-check.sh' -o -name 'nfqws' \
    \) -delete

cp "$root/LICENSE" "$stage/App/PocketStream/LICENSE.txt"
cp "$root/PRIVACY.md" "$stage/App/PocketStream/PRIVACY.md"
cp "$root/SECURITY.md" "$stage/App/PocketStream/SECURITY.md"
cp "$root/THIRD_PARTY_NOTICES.md" "$stage/App/PocketStream/THIRD_PARTY_NOTICES.md"

find "$stage" -type d -exec chmod 0755 {} +
find "$stage" -type f -exec chmod 0644 {} +
chmod 0755 \
    "$stage/App/PocketStream/launch.sh" \
    "$stage/App/PocketStream/pocketstream" \
    "$stage/App/PocketStream/ffmpeg/ffmpeg" \
    "$stage/App/PocketStream/zapret/tpws"

rm -f -- "$archive" "$archive.sha256"
(
    cd "$stage"
    find App -type f -print | LC_ALL=C sort | zip -q -X "$archive" -@
)

(
    cd "$release_dir"
    sha256sum "$(basename "$archive")" > "$(basename "$archive").sha256"
)

echo "Created $archive"
