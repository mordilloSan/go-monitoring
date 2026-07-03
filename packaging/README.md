# Packaging

Linux release packaging: the `.deb` package and the signed APT repository.

## What ships where

Every semver `v*` tag, for example `v1.2.3` or `v1.2.3-rc.1`, runs
[.github/workflows/release.yml](../.github/workflows/release.yml):

1. **build** — compiles the `linux/amd64` v2 binary and runs
   [scripts/build-deb.sh](../scripts/build-deb.sh), which assembles the `.deb`
   with the binary, a generated default `/etc/go-monitoring/config.json`
   (marked as a conffile so upgrades never overwrite user edits), the
   [systemd unit](systemd/go-monitoring.service), and the
   [deb maintainer scripts](deb/). The postinst enables and starts the service
   on fresh install and restarts it on upgrade (skipped when systemd is not
   running, e.g. in containers).
2. **publish** — uploads the tarball, `.deb`, and checksums to the GitHub
   release.
3. **apt-repo** — adds the new `.deb` to the APT repository on the `gh-pages`
   branch, regenerates/re-signs the metadata with
   [scripts/build-apt-repo.sh](../scripts/build-apt-repo.sh), and copies
   [install.sh](install.sh) to the site root. GitHub Pages serves the result
   at `https://mordillosan.github.io/go-monitoring/apt` and the one-line
   installer at `https://mordillosan.github.io/go-monitoring/install.sh`.

The `apt-repo` job skips itself (with a note in the workflow summary) when the
`APT_GPG_PRIVATE_KEY` secret is missing, so releases still work before the
one-time setup below is done.

Manual `workflow_dispatch` releases require an existing semver tag. Prerelease
tags are converted to Debian prerelease versions, so `v1.2.3-rc.1` packages as
`1.2.3~rc.1` and sorts before `1.2.3`.

## How the APT repository works

The repository is a static file tree — no server-side software:

```
apt/
├── gpg.key                          # armored public signing key
├── dists/stable/
│   ├── Release                      # index checksums + metadata
│   ├── Release.gpg                  # detached signature of Release
│   ├── InRelease                    # clearsigned Release (what modern apt fetches)
│   └── main/binary-amd64/
│       ├── Packages                 # per-.deb metadata and checksums
│       └── Packages.gz
└── pool/main/
    └── go-monitoring_<version>_amd64.deb
```

Trust chain: the user installs `gpg.key` into `/usr/share/keyrings/` and pins
it with `signed-by=` in the sources entry. `apt update` downloads `InRelease`,
verifies its signature against that key, then verifies the `Packages` index
against the checksums in `Release`, and each `.deb` against the checksums in
`Packages`. Old versions stay in `pool/`, so downgrades with
`apt install go-monitoring=<version>` keep working.

## One-time maintainer setup

1. Generate a signing key (no passphrase shown here; add one if you prefer and
   store it as `APT_GPG_PASSPHRASE`):

   ```sh
   gpg --batch --gen-key <<'EOF'
   %no-protection
   Key-Type: eddsa
   Key-Curve: ed25519
   Name-Real: go-monitoring APT signing key
   Name-Email: miguelgalizamariz@gmail.com
   Expire-Date: 0
   %commit
   EOF
   ```

2. Store the private key as a repository secret:

   ```sh
   gpg --armor --export-secret-keys "go-monitoring APT signing key" \
     | gh secret set APT_GPG_PRIVATE_KEY
   ```

   Keep an offline backup of the private key — losing it means every user has
   to re-import a new key.

3. Publish a release once so the workflow creates the `gh-pages` branch, then
   enable GitHub Pages for it:

   ```sh
   gh api repos/mordilloSan/go-monitoring/pages \
     -X POST -f build_type=legacy -f 'source[branch]=gh-pages' -f 'source[path]=/'
   ```

That's it — subsequent releases publish to the repository automatically.

## Rebuilding locally

```sh
make build OS=linux ARCH=amd64 GOAMD64=v2 NVML=true
./scripts/build-deb.sh ./go-monitoring dist v0.0.0-dev
mkdir -p repo/pool/main && cp dist/*.deb repo/pool/main/
./scripts/build-apt-repo.sh repo   # needs gnupg + apt-utils and a secret key in the keyring
```
