#!/usr/bin/env bash
# Build only the Android 32-bit ARMv7 binary: go-magic-android-armv7
#
# Thin wrapper around scripts/build-android.sh, kept as its own entry
# point because this is the one target that needs the Android NDK:
# Go requires external (cgo) linking for every Android target except
# arm64, so the final link uses the NDK r27 clang (API 24). See the
# header of build-android.sh for the full explanation and for how the
# NDK is located (ANDROID_NDK_HOME / ANDROID_SDK_ROOT / runner paths).
set -Eeuo pipefail
cd "$(dirname "$0")/.."
exec ./scripts/build-android.sh armv7 "$@"
