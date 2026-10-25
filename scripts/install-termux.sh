#!/usr/bin/env bash
# =============================================================================
# go-magic Termux / Android installer
# =============================================================================
# Installs a prebuilt go-magic Android binary into $PREFIX/bin as `magic`
# (no sudo, nothing written outside the Termux prefix).
#
# Usage (inside Termux):
#   ./scripts/install-termux.sh <binary>        # install a local file, e.g.
#                                               # go-magic-android-arm64 or
#                                               # go-magic-android-armv7
#                                               # downloaded from the GitHub
#                                               # Actions artifact
#                                               # "go-magic-binaries"
#   ./scripts/install-termux.sh --url <url>     # download, then install
#
# Architecture notes:
#   - arm64 devices (uname -m = aarch64) use go-magic-android-arm64.
#   - 32-bit devices (uname -m = armv7l / armv8l) use go-magic-android-armv7.
#     The static go-magic-linux-armv7 fallback also runs under Termux.
#   - `magic version` prints the binary's GOOS/GOARCH, so you can verify
#     you installed the right one.
#
# Building from source inside Termux instead also works:
#   pkg install golang nodejs git
#   git clone https://github.com/Mala980/go-magic && cd go-magic
#   git checkout feat/termux-android
#   (cd web && npm ci && npm run build)
#   CGO_ENABLED=0 go build -o $PREFIX/bin/magic ./cmd/magic   # arm64
# =============================================================================
set -euo pipefail

PREFIX_DIR="${PREFIX:-}"
if [[ -z "$PREFIX_DIR" ]]; then
    if [[ -n "${TERMUX_VERSION:-}" || "$HOME" == *"com.termux"* ]]; then
        PREFIX_DIR="$HOME/../usr"
    else
        echo "error: \$PREFIX is not set and this does not look like Termux." >&2
        echo "This installer is for Termux only; on other systems use scripts/install.sh" >&2
        echo "or copy the android binary somewhere on PATH manually." >&2
        exit 1
    fi
fi

BIN_DIR="$PREFIX_DIR/bin"
SRC=""
URL=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --url)
            [[ -n "${2:-}" ]] || { echo "error: --url requires a value" >&2; exit 1; }
            URL="$2"; shift ;;
        --bin-dir)
            [[ -n "${2:-}" ]] || { echo "error: --bin-dir requires a value" >&2; exit 1; }
            BIN_DIR="$2"; shift ;;
        -h|--help)
            sed -n '2,40p' "$0"; exit 0 ;;
        -*) echo "error: unknown argument: $1" >&2; exit 1 ;;
        *)  SRC="$1" ;;
    esac
    shift
done

# Recommended binary for this device (informational + used to sanity-check
# an explicitly passed file name).
case "$(uname -m)" in
    aarch64|arm64) WANT="arm64" ;;
    armv7l|armv7*|armv8l|arm*) WANT="armv7" ;;
    *) WANT="" ;;
esac

TMP_FILE=""
cleanup() { [[ -n "$TMP_FILE" && -e "$TMP_FILE" ]] && rm -f "$TMP_FILE"; return 0; }
trap cleanup EXIT

if [[ -n "$URL" ]]; then
    TMP_FILE="$(mktemp "${TMPDIR:-$PREFIX_DIR/tmp}/go-magic-install.XXXXXX")"
    echo "[INFO] downloading $URL"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 3 -o "$TMP_FILE" "$URL"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$TMP_FILE" "$URL"
    else
        echo "error: neither curl nor wget found (pkg install curl)" >&2; exit 1
    fi
    SRC="$TMP_FILE"
fi

if [[ -z "$SRC" || ! -f "$SRC" ]]; then
    echo "error: give the path of the downloaded go-magic-android-* binary," >&2
    echo "or pass --url <download-url>. See the header of this script." >&2
    exit 1
fi

case "$(basename "$SRC")" in
    *arm64*armv7*|*armv7*arm64*) : ;;
    *arm64*)
        [[ "$WANT" == "armv7" ]] && echo "[WARN] this is an arm64 binary but the device looks 32-bit (uname -m: $(uname -m)); it will not run" >&2 ;;
    *armv7*)
        : ;; # armv7 binaries also run on many arm64 setups via 32-bit support; no warning
    *) echo "[WARN] file name does not contain arm64/armv7; installing as-is" >&2 ;;
esac

size="$(wc -c <"$SRC" | tr -d ' ')"
if [[ "$size" -lt 1048576 ]]; then
    echo "error: $SRC is only $size bytes; a real go-magic binary is tens of MB." >&2
    echo "Did the download save an error page / artifact zip instead of the binary?" >&2
    echo "(GitHub Actions artifacts download as .zip: unzip it first.)" >&2
    exit 1
fi

mkdir -p "$BIN_DIR"
dest="$BIN_DIR/magic"
if command -v install >/dev/null 2>&1; then
    install -m 0755 "$SRC" "$dest"
else
    cp -f "$SRC" "$dest"; chmod 0755 "$dest"
fi
echo "[ OK ] installed: $dest"

if [[ ":${PATH}:" != *":${BIN_DIR}:"* ]]; then
    echo "[WARN] $BIN_DIR is not on PATH in this shell" >&2
fi

if out="$("$dest" version 2>&1)"; then
    echo "[ OK ] verification: $out"
else
    echo "[WARN] installed binary did not run ('magic version' failed):" >&2
    echo "$out" >&2
    echo "Most likely cause: wrong architecture for this device (need: ${WANT:-unknown})." >&2
    exit 1
fi

echo
echo "Next: magic setup   # initialize the configuration (config dir ~/.magic)"
echo "      magic chat    # start a conversation"
echo "      magic server  # start the Web console"
