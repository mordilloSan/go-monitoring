#!/bin/sh
# Non-destructive smoke test for a host where go-monitoring is already
# installed. It reads service/package state and calls the local command socket;
# it does not install, remove, restart, reload, or edit configuration.
set -eu

SERVICE="${GO_MONITORING_SERVICE:-go-monitoring.service}"
SOCKET="${GO_MONITORING_SOCKET:-/run/go-monitoring/agent.sock}"
CONFIG="${GO_MONITORING_CONFIG:-/etc/go-monitoring/config.json}"
BIN="${GO_MONITORING_BIN:-go-monitoring}"
CURL="${CURL:-curl}"
TIMEOUT="${GO_MONITORING_SMOKE_TIMEOUT:-5}"

pass() {
	printf 'ok: %s\n' "$1"
}

warn() {
	printf 'warn: %s\n' "$1" >&2
}

fail() {
	printf 'error: %s\n' "$1" >&2
	exit 1
}

need_command() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 not found"
}

curl_unix() {
	"$CURL" --fail --silent --show-error --max-time "$TIMEOUT" \
		--unix-socket "$SOCKET" "$@"
}

command_api() {
	command_name="$1"
	curl_unix \
		-H 'Content-Type: application/json' \
		-d "{\"command\":\"$command_name\",\"request_id\":\"smoke-$command_name\"}" \
		http://localhost/api/v1/command
}

need_command systemctl
need_command "$CURL"
need_command "$BIN"

if command -v dpkg >/dev/null 2>&1; then
	dpkg -s go-monitoring >/dev/null 2>&1 || fail "go-monitoring package is not installed according to dpkg"
	pass "dpkg package is installed"
else
	warn "dpkg not found; skipping package metadata check"
fi

systemctl is-active --quiet "$SERVICE" || {
	systemctl status "$SERVICE" --no-pager || true
	fail "$SERVICE is not active"
}
pass "$SERVICE is active"

version="$("$BIN" --version 2>/dev/null || true)"
if [ -n "$version" ]; then
	pass "$BIN version: $version"
else
	warn "$BIN --version produced no output"
fi

[ -S "$SOCKET" ] || fail "$SOCKET is not a unix socket"
pass "$SOCKET exists"

if [ -r "$CONFIG" ]; then
	grep -Fq "unix:$SOCKET" "$CONFIG" || fail "$CONFIG does not reference unix:$SOCKET"
	pass "$CONFIG references unix:$SOCKET"
else
	warn "$CONFIG is not readable; skipping config file check"
fi

health="$(curl_unix http://localhost/healthz)"
printf '%s\n' "$health" | grep -Fq '"healthy":true' || fail "healthz is not healthy: $health"
pass "healthz reports healthy"

commands="$(command_api commands.list)"
printf '%s\n' "$commands" | grep -Fq '"ok":true' || fail "commands.list failed: $commands"
printf '%s\n' "$commands" | grep -Fq '"config.get"' || fail "commands.list does not include config.get: $commands"
pass "command API lists commands"

config="$(command_api config.get)"
printf '%s\n' "$config" | grep -Fq '"ok":true' || fail "config.get failed: $config"
printf '%s\n' "$config" | grep -Fq "unix:$SOCKET" || fail "config.get does not expose unix:$SOCKET: $config"
pass "config.get exposes the expected command socket"

status="$(command_api status.get)"
printf '%s\n' "$status" | grep -Fq '"ok":true' || fail "status.get failed: $status"
pass "status.get responds"

printf 'go-monitoring installed smoke test passed\n'
