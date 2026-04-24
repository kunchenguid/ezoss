#!/bin/sh

set -eu

OWNER=${OWNER:-kunchenguid}
REPO=${REPO:-ezoss}
BINARY=${BINARY:-ezoss}
VERSION=${VERSION-latest}
GITHUB_API_BASE=${GITHUB_API_BASE:-https://api.github.com}
GITHUB_DOWNLOAD_BASE=${GITHUB_DOWNLOAD_BASE:-https://github.com}

# BIN_DIR is the real binary location. The default is a user-owned directory
# so future `ezoss update` runs can atomically replace the binary without
# needing sudo on the system PATH entry. LINK_DIR is the optional symlink
# location placed on the user's PATH; when not set we pick the first viable
# candidate, and when LINK_DIR resolves to the same path as BIN_DIR we skip
# the symlink step entirely.
BIN_DIR=${BIN_DIR-$HOME/.ezoss/bin}
LINK_DIR=${LINK_DIR-${EZOSS_LINK_DIR-}}

github_api() {
	curl -fsSL -H 'Accept: application/vnd.github+json' "$1"
}

verify_checksum() {
	checksum_file=$1
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 -c "$checksum_file"
		return 0
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c "$checksum_file"
		return 0
	fi

	printf 'missing checksum tool: need shasum or sha256sum\n' >&2
	exit 1
}

resolve_version() {
	if [ "$VERSION" != "latest" ]; then
		printf '%s' "$VERSION"
		return 0
	fi

	github_api "${GITHUB_API_BASE}/repos/${OWNER}/${REPO}/releases/latest" \
		| sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
		| head -n 1
}

resolve_path() {
	(cd "$1" 2>/dev/null && pwd -P)
}

usage() {
	cat <<EOF
Usage: install.sh [--version <tag>] [--bin-dir <dir>] [--link-dir <dir>] [-h|--help]

The real ${BINARY} binary is installed to --bin-dir. When --link-dir is set
(or auto-detected), a symlink is created there so the binary is on PATH while
remaining user-owned for atomic self-update.

Defaults:
  --bin-dir   \$HOME/.ezoss/bin
  --link-dir  \$HOME/.local/bin if it is on PATH, else /usr/local/bin

Environment overrides:
  OWNER, REPO, BINARY, BIN_DIR, LINK_DIR, VERSION,
  GITHUB_API_BASE, GITHUB_DOWNLOAD_BASE,
  EZOSS_LINK_DIR        - alias for LINK_DIR
  EZOSS_SKIP_DAEMON=1   - skip the post-install daemon install and restart
  EZOSS_SKIP_SYMLINK=1  - install only the real binary; do not create a symlink
EOF
}

missing_value() {
	[ "$#" -lt 2 ] && return 0
	case "$2" in
		-*) return 0 ;;
		*) return 1 ;;
	esac
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--version)
			if missing_value "$@"; then
				printf 'missing value for --version\n' >&2
				usage >&2
				exit 1
			fi
			VERSION=$2
			shift 2
			;;
		--bin-dir)
			if missing_value "$@"; then
				printf 'missing value for --bin-dir\n' >&2
				usage >&2
				exit 1
			fi
			BIN_DIR=$2
			shift 2
			;;
		--link-dir)
			if missing_value "$@"; then
				printf 'missing value for --link-dir\n' >&2
				usage >&2
				exit 1
			fi
			LINK_DIR=$2
			shift 2
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			printf 'unknown argument: %s\n' "$1" >&2
			usage >&2
			exit 1
			;;
	esac
done

if [ -z "$VERSION" ]; then
	printf 'version must not be empty\n' >&2
	exit 1
fi

if [ -z "$BIN_DIR" ]; then
	printf 'bin dir must not be empty\n' >&2
	exit 1
fi

uname_s=$(uname -s)
uname_m=$(uname -m)

case "$uname_s" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*)
		printf 'unsupported OS: %s\n' "$uname_s" >&2
		exit 1
		;;
esac

case "$uname_m" in
	x86_64|amd64) arch=amd64 ;;
	arm64|aarch64) arch=arm64 ;;
	*)
		printf 'unsupported architecture: %s\n' "$uname_m" >&2
		exit 1
		;;
esac

resolved_version=$(resolve_version)
if [ -z "$resolved_version" ]; then
	printf 'failed to resolve release version for %s/%s\n' "$OWNER" "$REPO" >&2
	exit 1
fi

archive_name="${BINARY}-${resolved_version}-${os}-${arch}.tar.gz"
checksums_url="${GITHUB_DOWNLOAD_BASE}/${OWNER}/${REPO}/releases/download/${resolved_version}/checksums.txt"
url="${GITHUB_DOWNLOAD_BASE}/${OWNER}/${REPO}/releases/download/${resolved_version}/${archive_name}"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT INT TERM

archive_path="$tmpdir/$archive_name"
checksums_path="$tmpdir/checksums.txt"
checksum_entry_path="$tmpdir/checksum.txt"

curl -fsSL "$checksums_url" -o "$checksums_path"
curl -fsSL "$url" -o "$archive_path"
checksum_line=$(grep "  ${archive_name}$" "$checksums_path" || true)
if [ -z "$checksum_line" ]; then
	printf 'checksum not found for %s in checksums.txt\n' "$archive_name" >&2
	exit 1
fi
checksum_value=${checksum_line%% *}
printf '%s  %s\n' "$checksum_value" "$archive_path" > "$checksum_entry_path"
verify_checksum "$checksum_entry_path"
tar -xzf "$archive_path" -C "$tmpdir"

binary_src="$tmpdir/${BINARY}-${resolved_version}-${os}-${arch}/${BINARY}"
binary_path="$BIN_DIR/$BINARY"

if ! mkdir -p "$BIN_DIR"; then
	printf 'could not create bin directory: %s\n' "$BIN_DIR" >&2
	exit 1
fi
install -m 0755 "$binary_src" "$binary_path"

printf 'installed %s to %s\n' "$BINARY" "$binary_path"

# Create the on-PATH symlink unless the user opted out, BIN_DIR already
# matches LINK_DIR, or we cannot pick a writable LINK_DIR. Failures here are
# non-fatal: the real binary is already in place at BIN_DIR.
create_symlink=1
if [ "${EZOSS_SKIP_SYMLINK:-}" = "1" ]; then
	create_symlink=0
fi

if [ "$create_symlink" = "1" ] && [ -z "$LINK_DIR" ]; then
	case ":${PATH:-}:" in
		*":$HOME/.local/bin:"*) LINK_DIR="$HOME/.local/bin" ;;
		*":/usr/local/bin:"*) LINK_DIR="/usr/local/bin" ;;
	esac
fi

if [ "$create_symlink" = "1" ] && [ -n "$LINK_DIR" ]; then
	real_bin_dir=$(resolve_path "$BIN_DIR" || echo "")
	real_link_dir=$(resolve_path "$LINK_DIR" 2>/dev/null || echo "")
	if [ -n "$real_bin_dir" ] && [ "$real_bin_dir" = "$real_link_dir" ]; then
		: # same dir, nothing to do
	else
		link_path="$LINK_DIR/$BINARY"
		if [ -w "$LINK_DIR" ] || (mkdir -p "$LINK_DIR" 2>/dev/null && [ -w "$LINK_DIR" ]); then
			rm -f "$link_path"
			if ln -s "$binary_path" "$link_path" 2>/dev/null; then
				printf 'symlink %s -> %s\n' "$link_path" "$binary_path"
			fi
		else
			printf 'note: %s is not writable; skipping symlink\n' "$LINK_DIR" >&2
		fi
	fi
fi

# Best-effort: register the daemon with the OS service manager and start it.
# Failures here are not fatal because the binary itself is installed and
# usable; the user can always re-run `ezoss daemon install` later.
if [ "${EZOSS_SKIP_DAEMON:-}" != "1" ]; then
	"$binary_path" daemon install >/dev/null 2>&1 || true
	"$binary_path" daemon restart >/dev/null 2>&1 || true
fi

case ":${PATH:-}:" in
	*":$BIN_DIR:"*) ;;
	*)
		if [ -z "${LINK_DIR:-}" ]; then
			printf 'add %s to your PATH and restart your terminal\n' "$BIN_DIR"
		fi
		;;
esac
