#!/usr/bin/env bash
set -euo pipefail

usage() {
	echo "Usage: $0 <binary> <output-dir> <version>" >&2
}

if [ "$#" -ne 3 ]; then
	usage
	exit 2
fi

binary="$1"
out_dir="$2"
version="$3"

if [ ! -x "$binary" ]; then
	echo "Binary is not executable: $binary" >&2
	exit 1
fi

version="$(printf '%s' "$version" | sed 's/^v//; s/-/./g; s/[^A-Za-z0-9.+~:]/./g')"
if [ -z "$version" ]; then
	echo "Package version is empty" >&2
	exit 1
fi

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT

pkgroot="$root/pkg"
mkdir -p \
	"$pkgroot/DEBIAN" \
	"$pkgroot/etc/go-monitoring" \
	"$pkgroot/lib/systemd/system" \
	"$pkgroot/usr/bin" \
	"$out_dir"

install -m 0755 "$binary" "$pkgroot/usr/bin/go-monitoring"
"$binary" config --print --config "$root/config.json" > "$pkgroot/etc/go-monitoring/config.json"
install -m 0644 packaging/systemd/go-monitoring.service "$pkgroot/lib/systemd/system/go-monitoring.service"
install -m 0644 packaging/deb/conffiles "$pkgroot/DEBIAN/conffiles"
install -m 0755 packaging/deb/postinst "$pkgroot/DEBIAN/postinst"
install -m 0755 packaging/deb/prerm "$pkgroot/DEBIAN/prerm"
install -m 0755 packaging/deb/postrm "$pkgroot/DEBIAN/postrm"

installed_size="$(( ($(stat -c '%s' "$binary") + 1023) / 1024 + 64 ))"
cat > "$pkgroot/DEBIAN/control" <<EOF
Package: go-monitoring
Version: $version
Section: admin
Priority: optional
Architecture: amd64
Maintainer: go-monitoring maintainers
Depends: systemd
Installed-Size: $installed_size
Homepage: https://github.com/mordilloSan/go-monitoring
Description: Lightweight Linux host monitoring agent
 go-monitoring collects local host metrics and serves a small HTTP API backed by
 a local SQLite history database.
EOF

dpkg-deb --build --root-owner-group "$pkgroot" "$out_dir/go-monitoring_${version}_amd64.deb"
