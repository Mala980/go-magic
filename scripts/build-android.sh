#!/usr/bin/env bash
# =============================================================================
# go-magic Android / Termux build script
# =============================================================================
# Builds the two Android binaries used on Termux/Android:
#
#   go-magic-android-arm64   GOOS=android GOARCH=arm64, CGO_ENABLED=0.
#                            go-magic has no cgo dependencies at all
#                            (SQLite is the pure-Go modernc.org/sqlite),
#                            and arm64 is the one Android target Go can
#                            link internally, so this binary is a bionic
#                            PIE with interpreter /system/bin/linker64
#                            and needs no NDK and no extra libraries.
#
#   go-magic-android-armv7   GOOS=android GOARCH=arm GOARM=7. Go requires
#                            external (cgo) linking for every Android
#                            target except arm64 ("android/arm requires
#                            external (cgo) linking, but cgo is not
#                            enabled"), so this target is linked with the
#                            Android NDK clang (API 24, matching Termux's
#                            minimum supported Android version). No Go
#                            dependency gains a cgo requirement; only the
#                            final link uses the NDK. When no NDK is
#                            found the script also emits a fallback:
#
#   go-magic-linux-armv7     GOOS=linux GOARCH=arm GOARM=7, CGO_ENABLED=0,
#                            statically linked. A static Linux ELF runs
#                            under Termux as well, so this is a usable
#                            fallback on armv7 devices when the NDK
#                            binary is unavailable.
#
# Usage:
#   scripts/build-android.sh                  # both Android targets (+fallback if no NDK)
#   scripts/build-android.sh arm64            # only android/arm64
#   scripts/build-android.sh armv7            # only android/armv7 (needs the NDK)
#   scripts/build-android.sh --dir ./dist     # output directory (default: ./dist)
#   scripts/build-android.sh --checksum       # also write SHA256SUMS.txt
#
# The NDK is located from, in order: $ANDROID_NDK_HOME, $ANDROID_NDK_ROOT,
# $ANDROID_HOME/ndk/<version>, $ANDROID_SDK_ROOT/ndk/<version>,
# ~/Android/Sdk/ndk/<version>, /usr/local/lib/android/sdk/ndk/<version>
# (the GitHub ubuntu runner image path). Install it with:
#   sdkmanager "ndk;27.2.12479018"     # NDK r27
# =============================================================================
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/common.sh
source "${SCRIPT_DIR}/lib/common.sh"

REPO_ROOT="$(gm_repo_root)"
cd "$REPO_ROOT"

VERSION="$(gm_resolve_version)"
OUT_DIR="${BUILD_DIR:-$REPO_ROOT/dist}"
CHECKSUM="false"
TARGETS=""

usage() {
    cat <<EOF
go-magic Android/Termux build

Usage: $0 [arm64|armv7 ...] [options]

Options:
  --version <v>  version (default: the git tag from git describe)
  --dir <path>   output directory (default: ./dist)
  --checksum     generate SHA256SUMS.txt in the output directory
  -h, --help     show help

Outputs:
  go-magic-android-arm64   android/arm64, CGO_ENABLED=0, /system/bin/linker64
  go-magic-android-armv7   android/arm + GOARM=7, NDK clang (API 24)
  go-magic-linux-armv7     linux/arm + GOARM=7, static (fallback, only
                           emitted together with armv7 when no NDK is found,
                           or when explicitly requested as linux-armv7)
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        arm64|android-arm64)   TARGETS="$TARGETS arm64" ;;
        armv7|android-armv7)   TARGETS="$TARGETS armv7" ;;
        linux-armv7)           TARGETS="$TARGETS linux-armv7" ;;
        --version)
            [[ -n "${2:-}" ]] || { gm_error "--version requires a value"; exit 1; }
            VERSION="$2"; shift ;;
        --dir)
            [[ -n "${2:-}" ]] || { gm_error "--dir requires a value"; exit 1; }
            OUT_DIR="$2"; shift ;;
        --checksum) CHECKSUM="true" ;;
        -h|--help) usage; exit 0 ;;
        *) gm_error "unknown argument: $1"; usage; exit 1 ;;
    esac
    shift
done

[[ -n "$TARGETS" ]] || TARGETS=" arm64 armv7"

gm_require_cmd go "please install Go 1.26+" || exit 1

if [[ -z "$OUT_DIR" || "$OUT_DIR" == "/" || ${#OUT_DIR} -lt 3 ]]; then
    gm_error "invalid output directory: '$OUT_DIR'"
    exit 1
fi
mkdir -p "$OUT_DIR"

# internal/server/dist is a go:embed target; build the Web UI first.
gm_ensure_web_dist || exit 1

find_ndk() {
    local candidates=(
        "${ANDROID_NDK_HOME:-}"
        "${ANDROID_NDK_ROOT:-}"
    )
    local roots=("${ANDROID_HOME:-}" "${ANDROID_SDK_ROOT:-}" "$HOME/Android/Sdk" "/usr/local/lib/android/sdk")
    local root d c
    for root in "${roots[@]}"; do
        [[ -n "$root" ]] || continue
        for d in "$root"/ndk/*/; do
            [[ -d "$d" ]] && candidates+=("${d%/}")
        done
    done
    for c in "${candidates[@]}"; do
        if [[ -n "$c" && -x "$c/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi24-clang" ]]; then
            printf '%s\n' "$c"; return 0
        fi
        if [[ -n "$c" && -x "$c/toolchains/llvm/prebuilt/darwin-x86_64/bin/armv7a-linux-androideabi24-clang" ]]; then
            printf '%s\n' "$c"; return 0
        fi
    done
    return 1
}

build_android_arm64() {
    local out="$OUT_DIR/go-magic-android-arm64"
    gm_info "building android/arm64 -> $out"
    CGO_ENABLED=0 GOOS=android GOARCH=arm64 \
        go build -ldflags "$(gm_ldflags "$VERSION")" -o "$out" ./cmd/magic
    [[ -f "$out" ]] || { gm_error "go build exited 0 but $out does not exist"; return 1; }
    gm_ok "created $out ($(gm_file_size "$out"))"
}

build_linux_armv7() {
    local out="$OUT_DIR/go-magic-linux-armv7"
    gm_info "building linux/arm GOARM=7 (static fallback) -> $out"
    CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
        go build -ldflags "$(gm_ldflags "$VERSION")" -o "$out" ./cmd/magic
    [[ -f "$out" ]] || { gm_error "go build exited 0 but $out does not exist"; return 1; }
    gm_ok "created $out ($(gm_file_size "$out"))"
}

build_android_armv7() {
    local api="${ANDROID_API:-24}"
    local ndk toolchain out
    ndk="$(find_ndk || true)"
    if [[ -z "$ndk" ]]; then
        gm_warn "Android NDK not found: android/armv7 cannot be linked without it"
        gm_warn "(Go requires external linking for android/arm; install NDK r27,"
        gm_warn " e.g. sdkmanager \"ndk;27.2.12479018\", or set ANDROID_NDK_HOME)"
        gm_warn "building the static linux/armv7 fallback instead"
        build_linux_armv7
        return 2
    fi
    toolchain="$ndk/toolchains/llvm/prebuilt/linux-x86_64/bin"
    [[ -x "$toolchain/armv7a-linux-androideabi${api}-clang" ]] || \
        toolchain="$ndk/toolchains/llvm/prebuilt/darwin-x86_64/bin"
    out="$OUT_DIR/go-magic-android-armv7"
    gm_info "building android/arm GOARM=7 (API $api, NDK $ndk) -> $out"
    CGO_ENABLED=1 GOOS=android GOARCH=arm GOARM=7 \
    CC="$toolchain/armv7a-linux-androideabi${api}-clang" \
    CXX="$toolchain/armv7a-linux-androideabi${api}-clang++" \
        go build -ldflags "$(gm_ldflags "$VERSION")" -o "$out" ./cmd/magic
    [[ -f "$out" ]] || { gm_error "go build exited 0 but $out does not exist"; return 1; }
    gm_ok "created $out ($(gm_file_size "$out"))"
}

gm_step "go-magic ${VERSION} -- Android targets -> $OUT_DIR"
FAILED=0
for t in $TARGETS; do
    case "$t" in
        arm64)       build_android_arm64 || FAILED=1 ;;
        armv7)
            rc=0; build_android_armv7 || rc=$?
            # rc=2 means "fallback built because no NDK": not a hard failure
            # for a local run, but CI sets REQUIRE_NDK=1 to make it one.
            if [[ "$rc" == "2" && "${REQUIRE_NDK:-0}" == "1" ]]; then
                gm_error "REQUIRE_NDK=1 but no Android NDK was found"
                FAILED=1
            elif [[ "$rc" != "0" && "$rc" != "2" ]]; then
                FAILED=1
            fi
            ;;
        linux-armv7) build_linux_armv7 || FAILED=1 ;;
    esac
done

if [[ "$CHECKSUM" == "true" ]]; then
    (
        cd "$OUT_DIR" || exit 1
        rm -f SHA256SUMS.txt
        sha256sum go-magic-* > SHA256SUMS.txt
    )
    gm_ok "created $OUT_DIR/SHA256SUMS.txt"
fi

[[ "$FAILED" == "0" ]] || { gm_error "Android build failed"; exit 1; }
gm_ok "all done"
