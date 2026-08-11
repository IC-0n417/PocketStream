#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$root"

required_go=go1.26
actual_go=$(go env GOVERSION)
case "$actual_go" in
    "$required_go".*) ;;
    *)
        echo "PocketStream release builds require Go 1.26.x; found $actual_go" >&2
        exit 1
        ;;
esac

go test ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...

CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
    go build -buildvcs=false -trimpath -mod=readonly \
    -ldflags='-s -w -buildid=' \
    -o dist/App/PocketStream/pocketstream ./cmd/pocketstream

chmod 0755 dist/App/PocketStream/pocketstream dist/App/PocketStream/launch.sh
echo "Built dist/App/PocketStream/pocketstream with $actual_go"
