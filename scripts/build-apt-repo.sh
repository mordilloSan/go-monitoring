#!/usr/bin/env bash
set -euo pipefail

# Regenerates the APT repository metadata for every .deb under <repo-dir>/pool
# and signs it. The signing key must already be in the gpg keyring; set
# GPG_KEY_ID to pick a key (defaults to the first secret key) and
# GPG_PASSPHRASE if the key is protected.

usage() {
	echo "Usage: $0 <repo-dir>" >&2
}

if [ "$#" -ne 1 ]; then
	usage
	exit 2
fi

repo_dir="$1"
suite="stable"
component="main"
arch="amd64"

if ! command -v apt-ftparchive >/dev/null 2>&1; then
	echo "apt-ftparchive not found (install apt-utils)" >&2
	exit 1
fi

if [ -z "$(find "$repo_dir/pool" -name '*.deb' -print -quit 2>/dev/null)" ]; then
	echo "No .deb files under $repo_dir/pool" >&2
	exit 1
fi

key_id="${GPG_KEY_ID:-$(gpg --list-secret-keys --with-colons | awk -F: '/^sec/ {print $5; exit}')}"
if [ -z "$key_id" ]; then
	echo "No GPG secret key available for signing" >&2
	exit 1
fi

gpg_sign() {
	if [ -n "${GPG_PASSPHRASE:-}" ]; then
		gpg --batch --yes --pinentry-mode loopback --passphrase "$GPG_PASSPHRASE" \
			--local-user "$key_id" "$@"
	else
		gpg --batch --yes --local-user "$key_id" "$@"
	fi
}

binary_dir="dists/$suite/$component/binary-$arch"
mkdir -p "$repo_dir/$binary_dir"

# Filename: entries in Packages must be relative to the repo root. Release is
# generated outside dists/ so apt-ftparchive never hashes a stale copy of
# itself or the old signatures.
(
	cd "$repo_dir"
	rm -f "dists/$suite/Release" "dists/$suite/Release.gpg" "dists/$suite/InRelease"
	apt-ftparchive --arch "$arch" packages pool > "$binary_dir/Packages"
	gzip -9 -k -f "$binary_dir/Packages"
	release_tmp="$(mktemp)"
	apt-ftparchive \
		-o "APT::FTPArchive::Release::Origin=go-monitoring" \
		-o "APT::FTPArchive::Release::Label=go-monitoring" \
		-o "APT::FTPArchive::Release::Suite=$suite" \
		-o "APT::FTPArchive::Release::Codename=$suite" \
		-o "APT::FTPArchive::Release::Architectures=$arch" \
		-o "APT::FTPArchive::Release::Components=$component" \
		release "dists/$suite" > "$release_tmp"
	mv "$release_tmp" "dists/$suite/Release"
)

gpg_sign --detach-sign --armor -o "$repo_dir/dists/$suite/Release.gpg" "$repo_dir/dists/$suite/Release"
gpg_sign --clearsign -o "$repo_dir/dists/$suite/InRelease" "$repo_dir/dists/$suite/Release"
gpg --batch --yes --armor --export "$key_id" > "$repo_dir/gpg.key"

echo "APT repository written to $repo_dir (suite=$suite component=$component arch=$arch key=$key_id)"
