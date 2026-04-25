#!/usr/bin/env sh
set -eu

mode="${1:-run-args}"

devices=""
smart_devices=""

has_device() {
	printf '%s\n' "$devices" | grep -qx "$1"
}

add_device() {
	path="$1"
	device_type="$2"

	if [ ! -e "$path" ] || has_device "$path"; then
		return 0
	fi

	devices="${devices}
${path}"
	if [ -z "$smart_devices" ]; then
		smart_devices="${path}:${device_type}"
	else
		smart_devices="${smart_devices},${path}:${device_type}"
	fi
}

discover_with_lsblk() {
	if ! command -v lsblk >/dev/null 2>&1; then
		return 1
	fi

	lsblk -ndo NAME,TYPE 2>/dev/null | while read -r name dev_type; do
		[ "$dev_type" = "disk" ] || continue
		case "$name" in
			nvme*n*)
				controller="$(printf '%s\n' "$name" | sed 's/n[0-9].*$//')"
				printf '%s nvme\n' "/dev/${controller}"
				;;
			sd*|hd*|xvd*|vd*)
				printf '%s sat\n' "/dev/${name}"
				;;
		esac
	done
}

discover_with_globs() {
	for path in /dev/nvme[0-9]*; do
		[ -e "$path" ] || continue
		case "$path" in
			/dev/nvme[0-9]|/dev/nvme[0-9][0-9]) printf '%s nvme\n' "$path" ;;
		esac
	done

	for path in /dev/sd[a-z] /dev/hd[a-z] /dev/xvd[a-z] /dev/vd[a-z]; do
		[ -e "$path" ] || continue
		printf '%s sat\n' "$path"
	done
}

discovered="$(discover_with_lsblk || true)"
if [ -z "$discovered" ]; then
	discovered="$(discover_with_globs || true)"
fi

while read -r path device_type; do
	[ -n "${path:-}" ] || continue
	add_device "$path" "$device_type"
done <<EOF
$discovered
EOF

case "$mode" in
	run-args)
		if [ -n "$smart_devices" ]; then
			printf '%s ' "--cap-add=SYS_RAWIO" "--cap-add=SYS_ADMIN"
			for path in $devices; do
				[ -n "$path" ] || continue
				printf '%s ' "--device=${path}:${path}"
			done
			printf -- '-e SMART_DEVICES=%s\n' "$smart_devices"
		fi
		;;
	compose)
		printf '%s\n' 'services:'
		printf '%s\n' '  go-monitoring:'
		if [ -n "$smart_devices" ]; then
			printf '%s\n' '    cap_add:'
			printf '%s\n' '      - SYS_RAWIO'
			printf '%s\n' '      - SYS_ADMIN'
			printf '%s\n' '    devices:'
			for path in $devices; do
				[ -n "$path" ] || continue
				printf '      - %s:%s\n' "$path" "$path"
			done
			printf '%s\n' '    environment:'
			printf '      SMART_DEVICES: "%s"\n' "$smart_devices"
		else
			printf '%s\n' '    environment:'
			printf '%s\n' '      SMART_DEVICES: ""'
		fi
		;;
	summary)
		if [ -n "$smart_devices" ]; then
			printf '%s\n' "$smart_devices"
		else
			printf '%s\n' "No SMART-capable host devices discovered."
		fi
		;;
	*)
		printf 'usage: %s [run-args|compose|summary]\n' "$0" >&2
		exit 2
		;;
esac
