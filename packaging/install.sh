#!/bin/sh
# go-monitoring installer: adds the signed APT repository and installs the
# package. Usage:
#   curl -fsSL https://mordillosan.github.io/go-monitoring/install.sh | sudo sh
set -eu

REPO_URL="https://mordillosan.github.io/go-monitoring/apt"
KEYRING="/etc/apt/keyrings/go-monitoring.asc"
LIST="/etc/apt/sources.list.d/go-monitoring.list"

if [ "$(id -u)" -ne 0 ]; then
	echo "This script must run as root. Try:" >&2
	echo "  curl -fsSL https://mordillosan.github.io/go-monitoring/install.sh | sudo sh" >&2
	exit 1
fi

if ! command -v apt-get >/dev/null 2>&1; then
	echo "apt-get not found; this installer supports Debian/Ubuntu only." >&2
	echo "Grab a release from https://github.com/mordilloSan/go-monitoring/releases instead." >&2
	exit 1
fi

arch="$(dpkg --print-architecture)"
if [ "$arch" != "amd64" ]; then
	echo "Only amd64 packages are published; this system is $arch." >&2
	exit 1
fi

echo "Adding APT repository..."
mkdir -p /etc/apt/keyrings
curl -fsSL "$REPO_URL/gpg.key" -o "$KEYRING"
echo "deb [signed-by=$KEYRING arch=amd64] $REPO_URL stable main" > "$LIST"

echo "Installing go-monitoring..."
apt-get update -qq
apt-get install -y go-monitoring

echo
echo "Done. The service is enabled and running:"
echo "  systemctl status go-monitoring.service"
echo "Config: /etc/go-monitoring/config.json (systemctl reload after changes)"
