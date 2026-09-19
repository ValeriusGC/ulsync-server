#!/bin/sh
# install.sh puts a static ulsync-server on Ubuntu and starts it.
# Pass exactly one of --jwks-url (existing login) or --shared-secret
# (single-user box). This script does not install Docker, does not
# overwrite YAML, and does not turn verification off.
#
# Why one command, not Docker: a stranger on blank Ubuntu is not an
# administrator of engines. curl | sh is the product; git clone is the
# rejected path. This script never installs an engine, never changes
# login groups, and it does not install a systemd unit — the process is
# the background job under $PREFIX, with a pid file, because the gate is
# a VM that may have no systemd product.
#
# Why YAML is sacred: after the first start the file on disk is the
# operator's. Repeating the one-liner (or passing a flag again) must
# not rewrite it, invent a secret, or move admin.bind off loopback.
#
# Why the two flags are XOR: merging would pick an authority silently.
# Neither flag on a missing config.yaml is not a "read config" stack;
# the message names --jwks-url and --shared-secret. Both at once is
# the same class of error. The process verifies tokens and does not
# accept a private PEM; there is no --jwks-file here because a blank
# host has no JWKS file yet.
#
# Why --bin: Machine (and a host without GitHub Releases) already has
# a binary. Without --bin this script downloads
# ulsync-server_linux_<arch> from releases/latest/download/. Names are
# frozen; GHCR is not the gate. x86_64 → amd64, aarch64/arm64 → arm64,
# anything else exits 1.

# Release asset names are frozen in DECISION_INSTALL; renaming breaks
# the step-41 workflow and this script together.
RELEASE_BASE='https://github.com/ValeriusGC/ulsync-server/releases/latest/download'
HEALTH_URL='http://127.0.0.1:8080/health'
HEALTH_WAIT_SECS=30

JWKS_URL=
SHARED_SECRET=
PREFIX=
BIN=

die() {
	echo "install.sh: $*" >&2
	exit 1
}

usage() {
	echo "install.sh: pass exactly one of --jwks-url or --shared-secret" >&2
	echo "  --jwks-url URL       seed a missing config from a public JWKS URL" >&2
	echo "  --shared-secret STR  seed a missing config from one HMAC string" >&2
	echo "  --prefix DIR         default \$HOME/.ulsync" >&2
	echo "  --bin PATH           use this binary; skip GitHub download" >&2
}

need_arg() {
	# $1 is the flag name, $2 is remaining argc after seeing the flag.
	if [ "$2" -lt 1 ]; then
		die "$1 requires a value"
	fi
}

while [ $# -gt 0 ]; do
	case "$1" in
	--jwks-url)
		need_arg "$1" $(($# - 1))
		JWKS_URL=$2
		shift 2
		;;
	--shared-secret)
		need_arg "$1" $(($# - 1))
		SHARED_SECRET=$2
		shift 2
		;;
	--prefix)
		need_arg "$1" $(($# - 1))
		PREFIX=$2
		shift 2
		;;
	--bin)
		need_arg "$1" $(($# - 1))
		BIN=$2
		shift 2
		;;
	*)
		usage
		die "unknown argument: $1"
		;;
	esac
done

# Both flags at once is never a merge: the script must not pick URL
# over secret (or the reverse) and start.
if [ -n "$JWKS_URL" ] && [ -n "$SHARED_SECRET" ]; then
	die "pass exactly one of --jwks-url or --shared-secret, not both"
fi

if [ -z "$PREFIX" ]; then
	PREFIX="${HOME:?HOME is not set}/.ulsync"
fi

command -v curl >/dev/null 2>&1 || die "curl is required to wait for /health (and to download without --bin)"

mkdir -p "$PREFIX" || die "cannot create $PREFIX"

BIN_DEST="$PREFIX/ulsync-server"
CONFIG="$PREFIX/config.yaml"
PIDFILE="$PREFIX/ulsync-server.pid"
LOG="$PREFIX/ulsync-server.log"

place_binary() {
	if [ -n "$BIN" ]; then
		[ -f "$BIN" ] || die "--bin is not a file: $BIN"
		if [ "$BIN" = "$BIN_DEST" ]; then
			chmod 0755 "$BIN_DEST" || die "chmod $BIN_DEST failed"
			return
		fi
		cp "$BIN" "$BIN_DEST" || die "copy --bin to $BIN_DEST failed"
		chmod 0755 "$BIN_DEST" || die "chmod $BIN_DEST failed"
		return
	fi
	machine=$(uname -m)
	case "$machine" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) die "unsupported architecture $machine (need x86_64 or aarch64/arm64)" ;;
	esac
	url="${RELEASE_BASE}/ulsync-server_linux_${arch}"
	curl -fL -o "$BIN_DEST" "$url" || die "download failed: $url"
	chmod 0755 "$BIN_DEST" || die "chmod $BIN_DEST failed"
}

place_binary

# Missing YAML and no seed flag: fail here so the operator sees the two
# flag names, not a wrapped "read config" from the binary. Do not create
# config.yaml on this path.
if [ ! -f "$CONFIG" ] && [ -z "$JWKS_URL" ] && [ -z "$SHARED_SECRET" ]; then
	die "config.yaml is missing under $PREFIX; pass exactly one of --jwks-url or --shared-secret"
fi

health_ok() {
	curl -fsS -o /dev/null --connect-timeout 1 "$HEALTH_URL" 2>/dev/null
}

wait_health() {
	_pid=$1
	_n=0
	while [ "$_n" -lt "$HEALTH_WAIT_SECS" ]; do
		if health_ok; then
			return 0
		fi
		if ! kill -0 "$_pid" 2>/dev/null; then
			echo "install.sh: ulsync-server (pid $_pid) exited before /health answered" >&2
			if [ -f "$LOG" ]; then
				cat "$LOG" >&2
			fi
			return 1
		fi
		_n=$((_n + 1))
		sleep 1
	done
	echo "install.sh: timed out waiting for $HEALTH_URL" >&2
	return 1
}

# A listener already serving /health means 8080 is taken by a live
# store. Do not start a second process. YAML is left byte-for-byte.
if health_ok; then
	echo "$HEALTH_URL"
	exit 0
fi

if [ -f "$PIDFILE" ]; then
	oldpid=$(cat "$PIDFILE") || oldpid=
	if [ -n "$oldpid" ] && kill -0 "$oldpid" 2>/dev/null; then
		wait_health "$oldpid" || exit 1
		echo "$HEALTH_URL"
		exit 0
	fi
fi

# Existing YAML: start from disk and do not pass seed flags, even if
# the one-liner still has --jwks-url / --shared-secret. Missing YAML:
# pass exactly one seed flag; the binary writes the file.
if [ -f "$CONFIG" ]; then
	"$BIN_DEST" -config "$CONFIG" >>"$LOG" 2>&1 &
else
	if [ -n "$JWKS_URL" ]; then
		"$BIN_DEST" -config "$CONFIG" -jwks-url "$JWKS_URL" >>"$LOG" 2>&1 &
	else
		"$BIN_DEST" -config "$CONFIG" -shared-secret "$SHARED_SECRET" >>"$LOG" 2>&1 &
	fi
fi
pid=$!
# Pid file is how this script finds the job later. Not a systemd unit.
echo "$pid" >"$PIDFILE" || die "cannot write $PIDFILE"

wait_health "$pid" || exit 1
echo "$HEALTH_URL"
exit 0
