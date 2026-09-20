#!/bin/sh
# install.sh puts a static ulsync-server on Ubuntu and starts it.
# Pass exactly one of --jwks-url (existing login) or --shared-secret
# (single-user box). This script does not install Docker, does not
# overwrite YAML, and does not turn verification off.
#
# Why one command, not Docker: a stranger on blank Ubuntu is not an
# administrator of engines. curl | sh is the product; git clone is the
# rejected path. This script never installs an engine and never changes
# login groups.
#
# Why systemd when we can: a VPS reboot must not kill the store. As
# root, with systemctl, a prefix under $HOME/.ulsync becomes a unit
# (ulsync-<name>.service) and enable --now. A second one-liner against
# a live pid-file store adopts the unit (stops the background job, then
# systemd starts it). No root or no systemd: background + pid file, and
# the script says reboot will drop the process. Lab paths (/tmp/...)
# stay pid-only so a test prefix does not land in /etc/systemd/system.
#
# Why YAML is sacred: after the first start the file on disk is the
# operator's. Repeating the one-liner (or passing a flag again) must
# not rewrite it, invent a secret, or move admin.bind off loopback.
# If this prefix already answers /health, the script must not replace
# the running file: Linux returns ETXTBSY ("Text file busy") on that
# write, and the operator's process is already the product.
#
# Why the two flags are XOR: merging would pick an authority silently.
# Neither flag on a missing config.yaml is not a "read config" stack;
# the message names --jwks-url and --shared-secret. Both at once is
# the same class of error. The process verifies tokens and does not
# accept a private PEM; there is no --jwks-file here because a blank
# host has no JWKS file yet.
#
# Why --prefix and --listen: one VPS holds several stores. $HOME/.ulsync
# is the parent of stores, never a store: files there would nest the
# next app inside the first. A name without a slash (--prefix notes)
# is $HOME/.ulsync/notes. A path with a slash is used as-is. Omit
# --prefix and the store is $HOME/.ulsync/default. Each store is a
# directory and a pair of ports. --listen is mail (/health, /v1/*);
# --admin-listen is the panel (keep loopback). A live /health on 8080
# is not success for a different prefix: GET /health must report
# storage.path under this prefix, and wait_health requires this pid
# still alive. Otherwise a neighbor on 8080 would make a colliding
# install look successful.
#
# Why uninstall: taking a store down is the same product as putting it
# up. --uninstall stops the unit and the pid-file job. --purge also
# deletes $PREFIX (SQLite included). Repeat install then seeds again.
# A product prefix also gets ulsync-<name>-uninstall on disk (k3s
# pattern): teardown works if GitHub is unreachable. This is not ten
# shell lines in a Hands paper.

# Release asset names are frozen in DECISION_INSTALL; renaming breaks
# the step-41 workflow and this script together.
RELEASE_BASE='https://github.com/ValeriusGC/ulsync-server/releases/latest/download'
HEALTH_WAIT_SECS=30

JWKS_URL=
SHARED_SECRET=
PREFIX=
BIN=
LISTEN=
ADMIN_LISTEN=
UNINSTALL=
PURGE=

die() {
	echo "install.sh: $*" >&2
	exit 1
}

usage() {
	echo "install.sh: pass exactly one of --jwks-url or --shared-secret" >&2
	echo "  --jwks-url URL         seed a missing config from a public JWKS URL" >&2
	echo "  --shared-secret STR    seed a missing config from one HMAC string" >&2
	echo "  --prefix NAME|DIR      name under \$HOME/.ulsync, or a path; default \$HOME/.ulsync/default" >&2
	echo "  --listen HOST:PORT     first-run sync port bind; omit → 0.0.0.0:8080 (phones, /health, /v1)" >&2
	echo "  --admin-listen HOST:PORT  first-run panel bind; omit → 127.0.0.1:8081 (this machine only)" >&2
	echo "  --bin PATH             use this binary; skip GitHub download" >&2
	echo "  --uninstall            stop this prefix (systemd unit and pid file); keep files" >&2
	echo "  --purge                with --uninstall, delete the store directory" >&2
	echo "  as root on systemd:    install enables ulsync-NAME.service so reboot keeps the store" >&2
	echo "  curl | sh cannot prompt (stdin is the script); omitted --listen is 0.0.0.0:8080" >&2
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
	--listen)
		need_arg "$1" $(($# - 1))
		LISTEN=$2
		shift 2
		;;
	--admin-listen)
		need_arg "$1" $(($# - 1))
		ADMIN_LISTEN=$2
		shift 2
		;;
	--bin)
		need_arg "$1" $(($# - 1))
		BIN=$2
		shift 2
		;;
	--uninstall)
		UNINSTALL=1
		shift
		;;
	--purge)
		PURGE=1
		shift
		;;
	--help|-h)
		usage
		exit 0
		;;
	*)
		usage
		die "unknown argument: $1"
		;;
	esac
done

if [ -n "$UNINSTALL" ]; then
	if [ -n "$JWKS_URL" ] || [ -n "$SHARED_SECRET" ] || [ -n "$BIN" ] || [ -n "$LISTEN" ] || [ -n "$ADMIN_LISTEN" ]; then
		die "--uninstall does not take --jwks-url, --shared-secret, --listen, --admin-listen, or --bin"
	fi
fi
if [ -n "$PURGE" ] && [ -z "$UNINSTALL" ]; then
	die "--purge requires --uninstall"
fi

# Both flags at once is never a merge: the script must not pick URL
# over secret (or the reverse) and start.
if [ -z "$UNINSTALL" ] && [ -n "$JWKS_URL" ] && [ -n "$SHARED_SECRET" ]; then
	die "pass exactly one of --jwks-url or --shared-secret, not both"
fi

# A bare name is a store under $HOME/.ulsync. The parent itself is
# never a store: a second --prefix would otherwise land inside the first.
if [ -z "$PREFIX" ]; then
	PREFIX="${HOME:?HOME is not set}/.ulsync/default"
else
	case "$PREFIX" in
	.|..) die "--prefix cannot be . or .." ;;
	/*|*/*) ;;
	*) PREFIX="${HOME:?HOME is not set}/.ulsync/$PREFIX" ;;
	esac
fi

if [ -z "$UNINSTALL" ]; then
	command -v curl >/dev/null 2>&1 || die "curl is required to wait for /health (and to download without --bin)"
	mkdir -p "$PREFIX" || die "cannot create $PREFIX"
	PREFIX=$(CDPATH= cd -P -- "$PREFIX" && pwd) || die "cannot resolve prefix to an absolute path"
elif [ -d "$PREFIX" ]; then
	PREFIX=$(CDPATH= cd -P -- "$PREFIX" && pwd) || die "cannot resolve prefix to an absolute path"
fi

# Darwin: $HOME may be /var/folders/... while cd -P PREFIX is /private/var/...
HOME_ABS=$(CDPATH= cd -P -- "${HOME:?HOME is not set}" && pwd) || die "cannot resolve HOME"

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

# yaml_bind prints host:port from the first bind: line under section $2.
# IPv4 host:port only; first-run --listen is not an IPv6 bracket address.
yaml_bind() {
	_file=$1
	_sec=$2
	awk -v sec="$_sec" '
		$0 ~ "^"sec":" {insec=1; next}
		insec && /^[^[:space:]#]/ {insec=0}
		insec {
			line=$0
			sub(/^[[:space:]]+/, "", line)
			if (line ~ /^bind:[[:space:]]*/) {
				sub(/^bind:[[:space:]]*/, "", line)
				gsub(/"/, "", line)
				gsub(/\047/, "", line)
				print line
				exit
			}
		}
	' "$_file"
}

# health_url_from_bind maps 0.0.0.0 (all interfaces) to loopback so curl
# from this script reaches the process it just started.
health_url_from_bind() {
	_bind=$1
	_port=${_bind##*:}
	_host=${_bind%:*}
	case "$_host" in
	0.0.0.0|::|'') _host=127.0.0.1 ;;
	esac
	echo "http://${_host}:${_port}/health"
}

resolve_health_url() {
	_bind=
	if [ -f "$CONFIG" ]; then
		_bind=$(yaml_bind "$CONFIG" server)
	else
		_bind=$LISTEN
	fi
	[ -n "$_bind" ] || _bind='0.0.0.0:8080'
	health_url_from_bind "$_bind"
}

health_ok() {
	# A 200 from some process on this URL is not enough: a neighbor may
	# already own the default port. storage.path must live under PREFIX.
	_body=$(curl -fsS --connect-timeout 1 "$HEALTH_URL" 2>/dev/null) || return 1
	_path=$(printf '%s' "$_body" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
	[ -n "$_path" ] || return 1
	case "$_path" in
	"$PREFIX"|"$PREFIX"/*) return 0 ;;
	esac
	return 1
}

wait_health() {
	_pid=$1
	_n=0
	while [ "$_n" -lt "$HEALTH_WAIT_SECS" ]; do
		if ! kill -0 "$_pid" 2>/dev/null; then
			echo "install.sh: ulsync-server (pid $_pid) exited before /health answered" >&2
			_mail=$(resolve_bind server "$LISTEN" "$DEFAULT_MAIL")
			if [ -f "$LOG" ] && grep -qi 'address already in use' "$LOG"; then
				echo "install.sh: sync port ${_mail} is already taken; pass --listen HOST:PORT" >&2
			fi
			if [ -f "$LOG" ]; then
				cat "$LOG" >&2
			fi
			return 1
		fi
		if health_ok; then
			return 0
		fi
		_n=$((_n + 1))
		sleep 1
	done
	echo "install.sh: timed out waiting for $HEALTH_URL" >&2
	return 1
}

# curl | sh cannot prompt: stdin is the script. Name the binds so a person
# who omitted --listen still sees 0.0.0.0:8080 / 127.0.0.1:8081. Do not
# open the hoster firewall here; that is not in the README yet.
DEFAULT_MAIL='0.0.0.0:8080'
DEFAULT_PANEL='127.0.0.1:8081'

resolve_bind() {
	_sec=$1
	_flag=$2
	_default=$3
	_bind=
	if [ -f "$CONFIG" ]; then
		_bind=$(yaml_bind "$CONFIG" "$_sec")
	fi
	[ -n "$_bind" ] || _bind=$_flag
	[ -n "$_bind" ] || _bind=$_default
	printf '%s' "$_bind"
}

prefix_flag() {
	case "$PREFIX" in
	"${HOME_ABS}/.ulsync"/*|"${HOME}/.ulsync"/*) basename "$PREFIX" ;;
	*) printf '%s' "$PREFIX" ;;
	esac
}

shell_quote() {
	printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"
}

# Next to the stores, or on PATH when root. Not inside $PREFIX: --purge
# deletes the store; the helper must outlive that directory. Lab
# --prefix /tmp/... writes nothing here (k3s analog: no stray
# /usr/local/bin helper from a test prefix).
uninstall_path() {
	is_product_prefix || {
		printf ''
		return 0
	}
	_base=$(unit_basename)
	case "$_base" in
	''|.|..|*[!A-Za-z0-9_-]*)
		printf ''
		return 0
		;;
	esac
	if [ "$(id -u)" -eq 0 ] && [ -d /usr/local/bin ]; then
		printf '/usr/local/bin/ulsync-%s-uninstall' "$_base"
	else
		printf '%s/.ulsync/ulsync-%s-uninstall' "$HOME_ABS" "$_base"
	fi
}

# k3s leaves k3s-uninstall.sh on disk so teardown does not need GitHub.
# curl | sh cannot prompt; this file is the undo.
write_uninstall_helper() {
	_path=$(uninstall_path)
	[ -n "$_path" ] || return 0
	mkdir -p "$(dirname "$_path")" || die "cannot create $(dirname "$_path")"
	_unit=$(unit_name)
	_prefix_q=$(shell_quote "$PREFIX")
	_pid_q=$(shell_quote "$PIDFILE")
	_unit_q=$(shell_quote "$_unit")
	_unitfile_q=$(shell_quote "/etc/systemd/system/${_unit}.service")
	_self_q=$(shell_quote "$_path")
	cat >"$_path" <<EOF
#!/bin/sh
# Generated by ulsync-server install.sh. Stops this store.
# Usage: $0 [--purge]
PREFIX=${_prefix_q}
PIDFILE=${_pid_q}
UNIT=${_unit_q}
UNITFILE=${_unitfile_q}
SELF=${_self_q}
PURGE=
for a in "\$@"; do
	case "\$a" in
	--purge) PURGE=1 ;;
	*) echo "usage: \$0 [--purge]" >&2; exit 1 ;;
	esac
done
if [ "\$(id -u)" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
	systemctl disable --now "\$UNIT" >/dev/null 2>&1 || true
	systemctl reset-failed "\$UNIT" >/dev/null 2>&1 || true
	rm -f "\$UNITFILE"
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
if [ -f "\$PIDFILE" ]; then
	old=\$(cat "\$PIDFILE") || old=
	if [ -n "\$old" ] && kill -0 "\$old" 2>/dev/null; then
		kill "\$old" 2>/dev/null || true
		sleep 1
	fi
	rm -f "\$PIDFILE"
fi
if [ -n "\$PURGE" ]; then
	rm -rf "\$PREFIX"
	parent=\$(dirname "\$PREFIX")
	rmdir "\$parent" 2>/dev/null || true
	rm -f "\$SELF"
else
	echo "ulsync: files kept at \$PREFIX  (pass --purge to delete)" >&2
fi
echo "ulsync: uninstalled \$PREFIX" >&2
EOF
	chmod 0755 "$_path" || die "chmod $_path failed"
	echo "install.sh: wrote ${_path}  (--purge deletes files)" >&2
}

remove_uninstall_helper() {
	_path=$(uninstall_path)
	[ -n "$_path" ] || return 0
	rm -f "$_path"
}

# curl | sh cannot prompt (stdin is the script). Headscale --auto and k3s
# print listen + next steps; they do not `read` on a pipe.
announce_plan() {
	_mail=$(resolve_bind server "$LISTEN" "$DEFAULT_MAIL")
	_panel=$(resolve_bind admin "$ADMIN_LISTEN" "$DEFAULT_PANEL")
	echo "install.sh: store $PREFIX" >&2
	if [ -f "$CONFIG" ]; then
		echo "install.sh: sync port is ${_mail}  (from config.yaml; --listen does not rewrite it)" >&2
		echo "install.sh: panel is ${_panel}  (from config.yaml; --admin-listen does not rewrite it)" >&2
	else
		if [ -n "$LISTEN" ]; then
			echo "install.sh: sync port will be ${_mail}" >&2
		else
			echo "install.sh: sync port will be ${_mail}  (default; pass --listen HOST:PORT to choose)" >&2
		fi
		if [ -n "$ADMIN_LISTEN" ]; then
			echo "install.sh: panel will be ${_panel}  (this computer only)" >&2
		else
			echo "install.sh: panel will be ${_panel}  (default; this computer only; pass --admin-listen HOST:PORT to choose)" >&2
		fi
	fi
	if can_systemd; then
		echo "install.sh: systemd will enable $(unit_name).service  (survives reboot)" >&2
	elif is_product_prefix; then
		echo "install.sh: not root or no systemd — after reboot the process is gone" >&2
	fi
}

announce_result() {
	_mail=$(resolve_bind server "$LISTEN" "$DEFAULT_MAIL")
	_panel=$(resolve_bind admin "$ADMIN_LISTEN" "$DEFAULT_PANEL")
	_flag=$(prefix_flag)
	_mail_port=${_mail##*:}
	_panel_port=${_panel##*:}
	_ip=$(hostname -I 2>/dev/null | awk '{print $1}')
	[ -n "$_ip" ] || _ip='YOUR-VPS-IP'
	_u=$(unit_name)
	_help=$(uninstall_path)
	echo "install.sh: installed" >&2
	echo "install.sh:   store     $PREFIX" >&2
	echo "install.sh:   sync port ${_mail}   $HEALTH_URL" >&2
	echo "install.sh:   panel     ${_panel}   http://127.0.0.1:${_panel_port}/  (ssh -L ${_panel_port}:127.0.0.1:${_panel_port})" >&2
	case "$_mail" in
	0.0.0.0:*|::*)
		echo "install.sh:   phones    http://${_ip}:${_mail_port}/health" >&2
		echo "install.sh:   firewall  open TCP ${_mail_port} at the hoster; do not publish TCP ${_panel_port}" >&2
		;;
	*)
		echo "install.sh:   phones    not from the internet (sync port is ${_mail})" >&2
		;;
	esac
	if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$_u" 2>/dev/null; then
		echo "install.sh:   reboot    ${_u}.service stays up  (systemctl status ${_u})" >&2
		echo "install.sh:   logs      journalctl -u ${_u} -n 50 --no-pager" >&2
	elif is_product_prefix; then
		echo "install.sh:   reboot    process dies; re-run this command, or install as root" >&2
	fi
	if [ -n "$_help" ] && [ -f "$_help" ]; then
		echo "install.sh:   stop      ${_help}" >&2
		echo "install.sh:   wipe      ${_help} --purge" >&2
		echo "install.sh:   or        curl -fsSL ${RELEASE_BASE}/install.sh | sh -s -- --uninstall --prefix ${_flag} [--purge]" >&2
	else
		echo "install.sh:   stop      curl -fsSL ${RELEASE_BASE}/install.sh | sh -s -- --uninstall --prefix ${_flag}" >&2
		echo "install.sh:   wipe      curl -fsSL ${RELEASE_BASE}/install.sh | sh -s -- --uninstall --prefix ${_flag} --purge" >&2
	fi
}

# Something else already serving this mail URL is not a seed: name the
# bind and --listen before we download or spawn (PocketBase: busy port
# → pick another; we cannot prompt on curl | sh).
preflight_mail() {
	if health_ok; then
		return 0
	fi
	_mail=$(resolve_bind server "$LISTEN" "$DEFAULT_MAIL")
	_url=$(health_url_from_bind "$_mail")
	if curl -fsS --connect-timeout 1 "$_url" >/dev/null 2>&1; then
		die "sync port ${_mail} already answers (not this store); pass --listen HOST:PORT"
	fi
}

unit_basename() {
	_base=$(basename "$PREFIX")
	printf '%s' "$_base"
}

unit_name() {
	printf 'ulsync-%s' "$(unit_basename)"
}

# Product store on a real home, not a lab --prefix /tmp/... and not a
# HOME-override test. Those must not write /etc/systemd/system.
can_systemd() {
	command -v systemctl >/dev/null 2>&1 || return 1
	[ "$(id -u)" -eq 0 ] || return 1
	[ -d /etc/systemd/system ] || return 1
	case "$HOME" in
	/root|/home/*) ;;
	*) return 1 ;;
	esac
	case "$PREFIX" in
	"${HOME_ABS}/.ulsync"/*|"${HOME}/.ulsync"/*) ;;
	*) return 1 ;;
	esac
	_base=$(unit_basename)
	case "$_base" in
	''|.|..|*[!A-Za-z0-9_-]*) return 1 ;;
	esac
	return 0
}

is_product_prefix() {
	case "$PREFIX" in
	"${HOME_ABS}/.ulsync"/*|"${HOME}/.ulsync"/*) return 0 ;;
	esac
	return 1
}

write_unit() {
	_unitfile=$1
	_base=$(unit_basename)
	_owner=root
	_group=root
	if _u=$(stat -c '%U' "$PREFIX" 2>/dev/null); then
		_owner=$_u
	fi
	if _g=$(stat -c '%G' "$PREFIX" 2>/dev/null); then
		_group=$_g
	fi
	{
		echo "[Unit]"
		echo "Description=ulsync-server ${_base}"
		echo "After=network.target"
		echo
		echo "[Service]"
		echo "Type=simple"
		echo "User=${_owner}"
		echo "Group=${_group}"
		echo "WorkingDirectory=${PREFIX}"
		echo "ExecStart=${BIN_DEST} -config ${CONFIG}"
		echo "Restart=always"
		echo "RestartSec=5s"
		echo "LimitNOFILE=65535"
		echo
		echo "[Install]"
		echo "WantedBy=multi-user.target"
	} >"$_unitfile" || die "cannot write $_unitfile"
}

wait_health_loose() {
	_pid=$1
	if [ -n "$_pid" ] && [ "$_pid" != 0 ]; then
		wait_health "$_pid"
		return $?
	fi
	_n=0
	while [ "$_n" -lt "$HEALTH_WAIT_SECS" ]; do
		if health_ok; then
			return 0
		fi
		_n=$((_n + 1))
		sleep 1
	done
	echo "install.sh: timed out waiting for $HEALTH_URL" >&2
	return 1
}

stop_pidfile_job() {
	if [ ! -f "$PIDFILE" ]; then
		return 0
	fi
	_old=$(cat "$PIDFILE") || _old=
	if [ -n "$_old" ] && kill -0 "$_old" 2>/dev/null; then
		kill "$_old" 2>/dev/null || true
		sleep 1
	fi
	rm -f "$PIDFILE"
}

drop_unit() {
	command -v systemctl >/dev/null 2>&1 || return 0
	_base=$(unit_basename)
	case "$_base" in
	''|.|..|*[!A-Za-z0-9_-]*) return 0 ;;
	esac
	_unit=$(unit_name)
	_unitfile="/etc/systemd/system/${_unit}.service"
	if [ -f "$_unitfile" ] || systemctl is-active --quiet "$_unit" 2>/dev/null; then
		systemctl disable --now "$_unit" >/dev/null 2>&1 || true
		systemctl reset-failed "$_unit" >/dev/null 2>&1 || true
		rm -f "$_unitfile"
		systemctl daemon-reload >/dev/null 2>&1 || true
		echo "install.sh: removed ${_unit}" >&2
	fi
}

do_uninstall() {
	case "$PREFIX" in
	/|"$HOME") die "refusing to uninstall $PREFIX" ;;
	esac
	_flag=$(prefix_flag)
	drop_unit
	stop_pidfile_job
	if [ -n "$PURGE" ]; then
		if [ -d "$PREFIX" ]; then
			rm -rf "$PREFIX" || die "cannot delete $PREFIX"
			echo "install.sh: uninstalled ${_flag}" >&2
			echo "install.sh:   files     deleted $PREFIX" >&2
		else
			echo "install.sh: uninstalled ${_flag}" >&2
			echo "install.sh:   files     $PREFIX already gone" >&2
		fi
		_parent=$(dirname "$PREFIX")
		if [ "$_parent" = "${HOME_ABS}/.ulsync" ] || [ "$_parent" = "${HOME}/.ulsync" ]; then
			rmdir "$_parent" 2>/dev/null || true
		fi
	else
		echo "install.sh: uninstalled ${_flag}" >&2
		echo "install.sh:   files     kept at $PREFIX  (add --purge to delete)" >&2
	fi
	if [ -n "$PURGE" ]; then
		remove_uninstall_helper
	fi
	echo "install.sh:   install    curl -fsSL ${RELEASE_BASE}/install.sh | sh -s -- --prefix ${_flag} --jwks-url URL" >&2
	echo "install.sh:              or --shared-secret STR; other ports: --listen / --admin-listen" >&2
}

# Root + systemd + $HOME/.ulsync/<name>: the one-liner is what survives
# reboot. A live background job is replaced by the unit so two processes
# do not fight for the port.
adopt_systemd() {
	if ! can_systemd; then
		return 0
	fi
	_unit=$(unit_name)
	_unitfile="/etc/systemd/system/${_unit}.service"
	echo "install.sh: systemd: creating ${_unit}.service" >&2
	write_unit "$_unitfile"
	if systemctl is-active --quiet "$_unit"; then
		echo "install.sh: systemd: ${_unit}.service already running" >&2
		return 0
	fi
	stop_pidfile_job
	echo "install.sh: systemd: enabling ${_unit}.service" >&2
	systemctl daemon-reload || die "systemctl daemon-reload failed"
	echo "install.sh: systemd: starting ${_unit}.service" >&2
	systemctl enable --now "$_unit" || die "systemctl enable --now $_unit failed"
	_mpid=$(systemctl show -p MainPID --value "$_unit" 2>/dev/null) || _mpid=
	wait_health_loose "$_mpid" || exit 1
}

finish_ok() {
	adopt_systemd
	write_uninstall_helper
	announce_result
	echo "$HEALTH_URL"
	exit 0
}

if [ -n "$UNINSTALL" ]; then
	do_uninstall
	exit 0
fi

HEALTH_URL=$(resolve_health_url)

# A listener already serving THIS prefix's bind means the store is up.
# Do not copy or download onto the running file (Linux ETXTBSY).
# A foreign process on 8080 is not success for a different prefix or port.
if [ -f "$CONFIG" ] && health_ok; then
	finish_ok
fi

if [ -f "$PIDFILE" ]; then
	oldpid=$(cat "$PIDFILE") || oldpid=
	if [ -n "$oldpid" ] && kill -0 "$oldpid" 2>/dev/null; then
		wait_health "$oldpid" || exit 1
		finish_ok
	fi
fi

# Missing YAML and no seed flag: fail here so the operator sees the two
# flag names, not a wrapped "read config" from the binary. Do not create
# config.yaml on this path. Do not replace a binary first.
if [ ! -f "$CONFIG" ] && [ -z "$JWKS_URL" ] && [ -z "$SHARED_SECRET" ]; then
	die "config.yaml is missing under $PREFIX; pass exactly one of --jwks-url or --shared-secret"
fi

announce_plan
preflight_mail
place_binary

# Existing YAML: start from disk and do not pass seed flags, even if
# the one-liner still has --jwks-url / --shared-secret / --listen.
# Missing YAML: pass exactly one seed flag plus optional listen flags;
# the binary writes the file.
if [ -f "$CONFIG" ]; then
	"$BIN_DEST" -config "$CONFIG" >>"$LOG" 2>&1 &
else
	set -- -config "$CONFIG"
	if [ -n "$JWKS_URL" ]; then
		set -- "$@" -jwks-url "$JWKS_URL"
	else
		set -- "$@" -shared-secret "$SHARED_SECRET"
	fi
	[ -n "$LISTEN" ] && set -- "$@" -listen "$LISTEN"
	[ -n "$ADMIN_LISTEN" ] && set -- "$@" -admin-listen "$ADMIN_LISTEN"
	"$BIN_DEST" "$@" >>"$LOG" 2>&1 &
fi
pid=$!
echo "$pid" >"$PIDFILE" || die "cannot write $PIDFILE"

wait_health "$pid" || exit 1
finish_ok
