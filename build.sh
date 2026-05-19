#!/usr/bin/env bash
# build.sh — compile the authd daemon and bundle release artifacts.
#
# Steps:
#   1. Re-generate the gRPC stub from proto/v1/auth.proto so binary and
#      proto stay in sync.
#   2. go build the daemon binary into the stage directory, injecting
#      Version/Commit/Date via -ldflags -X.
#   3. Stage runtime assets (toml examples) alongside the binary and tar
#      the result.
#
# Usage:
#   ./build.sh                            # linux/amd64
#   ./build.sh --arm64                    # cross-compile linux/arm64
#   ./build.sh --dev                      # stamp full UTC datetime instead of YYMMDD
#   ./build.sh --build-dir=/path/to/out   # override build output dir
#
# Requires: protoc, protoc-gen-go, protoc-gen-go-grpc, go.

set -euo pipefail

GOARCH_TARGET="amd64"
BUILD_DIR="/tmp/authd/build"
DEV_DATE=0
for arg in "$@"; do
    case "$arg" in
        --arm64)
            GOARCH_TARGET="arm64"
            ;;
        --amd64)
            GOARCH_TARGET="amd64"
            ;;
        --dev)
            DEV_DATE=1
            ;;
        --build-dir=*)
            BUILD_DIR="${arg#--build-dir=}"
            if [[ -z "${BUILD_DIR}" ]]; then
                echo "--build-dir requires a non-empty path" >&2
                exit 1
            fi
            ;;
        -h|--help)
            sed -n '2,19p' "$0"
            exit 0
            ;;
        *)
            echo "unknown argument: $arg" >&2
            echo "usage: $0 [--arm64|--amd64] [--dev] [--build-dir=<path>]" >&2
            exit 1
            ;;
    esac
done

cd "$(dirname "$0")"

VERSION="$(git describe --tags --abbrev=0 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
if [ "${DEV_DATE}" = "1" ]; then
    DATE="$(date -u +%y%m%dT%H%M%SZ)"
else
    DATE="$(date -u +%y%m%d)"
fi

VERSION_PKG="authd/pkg/version"
LDFLAGS="-s -w \
    -X ${VERSION_PKG}.Version=${VERSION} \
    -X ${VERSION_PKG}.Commit=${COMMIT} \
    -X ${VERSION_PKG}.Date=${DATE}"

PROJECT_ROOT="$(pwd)"
PROTO_DIR="${PROJECT_ROOT}/proto/v1"
STUB_DIR="${PROJECT_ROOT}/pkg/grpc/auth"
STAGE_DIR="${BUILD_DIR}/authd"

echo "[1/4] regenerating proto stub from ${PROTO_DIR}/auth.proto"
mkdir -p "${STUB_DIR}"
protoc \
    --go_out="${STUB_DIR}"      --go_opt=paths=source_relative \
    --go-grpc_out="${STUB_DIR}" --go-grpc_opt=paths=source_relative \
    -I"${PROTO_DIR}" \
    "${PROTO_DIR}/auth.proto"

echo "[2/4] staging release directory at ${STAGE_DIR}"
mkdir -p "${BUILD_DIR}"
rm -rf "${STAGE_DIR}"
mkdir -p "${STAGE_DIR}"

echo "[3/4] building authd binary into stage (linux/${GOARCH_TARGET}) version=${VERSION} commit=${COMMIT} date=${DATE}"
CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH_TARGET}" \
    go build -trimpath -ldflags="${LDFLAGS}" -o "${STAGE_DIR}/authd" .
cp "${PROJECT_ROOT}/auth.toml.example"          "${STAGE_DIR}/auth.toml.example"
cp "${PROJECT_ROOT}/auth.settings.toml.example" "${STAGE_DIR}/auth.settings.toml.example"

ARCHIVE="${BUILD_DIR}/authd.tar.gz"
echo "[4/4] packing ${ARCHIVE}"
tar -czf "${ARCHIVE}" -C "${BUILD_DIR}" authd

echo "done."
echo "  staged:   ${STAGE_DIR}"
echo "  archive:  ${ARCHIVE}"
