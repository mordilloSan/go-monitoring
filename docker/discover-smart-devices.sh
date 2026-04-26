#!/usr/bin/env sh
set -eu

mode="${1:-run-args}"

devices=""
smart_devices=""
gpu_devices=""
caps=""

truthy() {
	case "$1" in
		1|true|TRUE|yes|YES|on|ON)
			return 0
			;;
	esac
	return 1
}

falsey() {
	case "$1" in
		0|false|FALSE|no|NO|off|OFF)
			return 0
			;;
	esac
	return 1
}

gpu_forced() {
	truthy "${DOCKER_GPU:-auto}"
}

gpu_disabled() {
	falsey "${DOCKER_GPU:-auto}"
}

nvidia_gpu_detected() {
	if command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L >/dev/null 2>&1; then
		return 0
	fi
	if [ ! -e /dev/nvidiactl ]; then
		return 1
	fi
	for path in /dev/nvidia[0-9]*; do
		[ -e "$path" ] && return 0
	done
	return 1
}

docker_nvidia_gpu_enabled() {
	if gpu_disabled; then
		return 1
	fi
	if gpu_forced; then
		return 0
	fi
	nvidia_gpu_detected
}

drm_vendor_detected() {
	vendor_id="$1"
	for vendor_path in /sys/class/drm/card*/device/vendor; do
		[ -e "$vendor_path" ] || continue
		vendor="$(cat "$vendor_path" 2>/dev/null || true)"
		[ "$vendor" = "$vendor_id" ] && return 0
	done
	return 1
}

intel_gpu_detected() {
	drm_vendor_detected 0x8086
}

amd_gpu_detected() {
	drm_vendor_detected 0x1002
}

has_dri_devices() {
	for path in /dev/dri/card* /dev/dri/renderD*; do
		[ -e "$path" ] && return 0
	done
	return 1
}

docker_drm_gpu_enabled() {
	if gpu_disabled; then
		return 1
	fi
	if gpu_forced; then
		has_dri_devices
		return
	fi
	intel_gpu_detected || amd_gpu_detected
}

docker_intel_gpu_enabled() {
	if gpu_disabled; then
		return 1
	fi
	if gpu_forced; then
		has_dri_devices
		return
	fi
	intel_gpu_detected
}

docker_amd_gpu_enabled() {
	if gpu_disabled; then
		return 1
	fi
	if gpu_forced; then
		[ -e /dev/kfd ] || has_dri_devices
		return
	fi
	amd_gpu_detected
}

has_cap() {
	printf '%s\n' "$caps" | grep -qx "$1"
}

add_cap() {
	cap="$1"
	if has_cap "$cap"; then
		return 0
	fi
	caps="${caps}
${cap}"
}

has_gpu_device() {
	printf '%s\n' "$gpu_devices" | grep -qx "$1"
}

add_gpu_device() {
	path="$1"
	if [ ! -e "$path" ] || has_gpu_device "$path"; then
		return 0
	fi
	gpu_devices="${gpu_devices}
${path}"
}

add_dri_devices() {
	for path in /dev/dri/card* /dev/dri/renderD*; do
		[ -e "$path" ] || continue
		add_gpu_device "$path"
	done
}

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
nvidia_gpu=false
if docker_nvidia_gpu_enabled; then
	nvidia_gpu=true
fi
if docker_drm_gpu_enabled; then
	add_dri_devices
fi
intel_gpu=false
if docker_intel_gpu_enabled; then
	intel_gpu=true
	add_cap PERFMON
fi
amd_gpu=false
if docker_amd_gpu_enabled; then
	amd_gpu=true
	add_gpu_device /dev/kfd
fi

while read -r path device_type; do
	[ -n "${path:-}" ] || continue
	add_device "$path" "$device_type"
done <<EOF
$discovered
EOF

if [ -n "$smart_devices" ]; then
	add_cap SYS_RAWIO
	add_cap SYS_ADMIN
fi

case "$mode" in
	run-args)
		printed=false
		if [ "$nvidia_gpu" = true ]; then
			printf '%s ' "--gpus=all"
			printf '%s ' "-e" "NVIDIA_VISIBLE_DEVICES=${NVIDIA_VISIBLE_DEVICES:-all}"
			printf '%s ' "-e" "NVIDIA_DRIVER_CAPABILITIES=${NVIDIA_DRIVER_CAPABILITIES:-compute,utility}"
			printed=true
		fi
		for cap in $caps; do
			[ -n "$cap" ] || continue
			printf '%s ' "--cap-add=${cap}"
			printed=true
		done
		if [ -n "$smart_devices" ]; then
			for path in $devices; do
				[ -n "$path" ] || continue
				printf '%s ' "--device=${path}:${path}"
			done
			printed=true
		fi
		for path in $gpu_devices; do
			[ -n "$path" ] || continue
			printf '%s ' "--device=${path}:${path}"
			printed=true
		done
		if [ -n "$smart_devices" ]; then
			printf -- '-e SMART_DEVICES=%s\n' "$smart_devices"
		fi
		if [ "$printed" = true ] && [ -z "$smart_devices" ]; then
			printf '\n'
		fi
		;;
	compose)
		printf '%s\n' 'services:'
		printf '%s\n' '  go-monitoring:'
		if [ "$nvidia_gpu" = true ]; then
			printf '%s\n' '    gpus: all'
		fi
		if [ -n "$caps" ]; then
			printf '%s\n' '    cap_add:'
			for cap in $caps; do
				[ -n "$cap" ] || continue
				printf '      - %s\n' "$cap"
			done
		fi
		if [ -n "$smart_devices" ] || [ -n "$gpu_devices" ]; then
			printf '%s\n' '    devices:'
			for path in $devices; do
				[ -n "$path" ] || continue
				printf '      - %s:%s\n' "$path" "$path"
			done
			for path in $gpu_devices; do
				[ -n "$path" ] || continue
				printf '      - %s:%s\n' "$path" "$path"
			done
		fi
		printf '%s\n' '    environment:'
		if [ -n "$smart_devices" ]; then
			printf '      SMART_DEVICES: "%s"\n' "$smart_devices"
		else
			printf '%s\n' '      SMART_DEVICES: ""'
		fi
		if [ "$nvidia_gpu" = true ]; then
			printf '%s\n' '      NVIDIA_VISIBLE_DEVICES: "${NVIDIA_VISIBLE_DEVICES:-all}"'
			printf '%s\n' '      NVIDIA_DRIVER_CAPABILITIES: "${NVIDIA_DRIVER_CAPABILITIES:-compute,utility}"'
		fi
		;;
	summary)
		if [ -n "$smart_devices" ]; then
			printf '%s\n' "$smart_devices"
		else
			printf '%s\n' "No SMART-capable host devices discovered."
		fi
		if [ "$nvidia_gpu" = true ]; then
			printf '%s\n' "NVIDIA GPU access will be requested for Compose."
		fi
		if [ "$intel_gpu" = true ]; then
			printf '%s\n' "Intel GPU access will be requested for Compose."
		fi
		if [ "$amd_gpu" = true ]; then
			printf '%s\n' "AMD GPU access will be requested for Compose."
		fi
		;;
	*)
		printf 'usage: %s [run-args|compose|summary]\n' "$0" >&2
		exit 2
		;;
esac
