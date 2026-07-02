[![Go Report Card](https://goreportcard.com/badge/github.com/mordilloSan/go-monitoring)](https://goreportcard.com/report/github.com/mordilloSan/go-monitoring)
[![License](https://img.shields.io/github/license/mordilloSan/go-monitoring)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/mordilloSan/go-monitoring)](go.mod)
[![CodeQL](https://github.com/mordilloSan/go-monitoring/actions/workflows/github-code-scanning/codeql/badge.svg)](https://github.com/mordilloSan/go-monitoring/actions/workflows/github-code-scanning/codeql)

# go-monitoring

A standalone Go agent that collects local system metrics, persists them to an embedded SQLite store, and exposes them over an HTTP JSON API.

## Upstream

This repository is a fork and derivative of [Beszel](https://github.com/henrygd/beszel) by henrygd. It is not a from-scratch monitoring agent; the codebase builds on Beszel's original agent work, with this repository's local changes layered on top.

Upstream Beszel is MIT-licensed. This fork's combined work is distributed under GPL-2.0-only; upstream Beszel-originated portions retain their MIT copyright and permission notice in [NOTICE](NOTICE).

## Features

- **System metrics** — CPU, memory, load, uptime, temperatures and sensors
- **Storage** — disks, filesystems, SMART attributes, mdraid, eMMC, ZFS ARC stats
- **GPU** — NVIDIA (NVML), AMD, Intel, Apple (Darwin), with `nvtop` fallback
- **Network** — per-interface bandwidth and I/O deltas
- **Containers** — Docker stats and container history
- **Services** — systemd unit state (Linux)
- **Persistence** — embedded SQLite store with rollups for history queries
- **HTTP API** — health, summary, history, containers, systemd, SMART

## Layout

- [cmd/go-monitoring/](cmd/go-monitoring/) — entrypoint (`main` package)
- [internal/app/](internal/app/) — app lifecycle, collection orchestration, and remaining local samplers/managers
- [internal/api/http/](internal/api/http/) — REST API routes, handlers, request logging, and query parsing
- [internal/api/model/](internal/api/model/) — REST API response contracts
- [internal/integration/docker/](internal/integration/docker/) — Docker/Podman integration
- [internal/integration/docker/dockerapi/](internal/integration/docker/dockerapi/) — Docker Engine API wire DTOs
- [internal/domain/](internal/domain/) — shared domain data types (system, container, smart, systemd)
- [internal/store/](internal/store/) — SQLite persistence, history, and rollups
- [internal/health/](internal/health/) — freshness check used by `go-monitoring health`
- [internal/version/](internal/version/) — version/app metadata
- [internal/common/](internal/common/), [internal/utils/](internal/utils/), [internal/deltatracker/](internal/deltatracker/) — shared helpers

## Build

Requires Go (see [go.mod](go.mod) for the pinned toolchain).

```sh
make build        # produces ./go-monitoring
make test         # runs the full backend check suite
make check-backend
make test-backend # runs Go unit tests only
make golint       # golangci-lint formatters + go mod tidy + modernize + golangci-lint
make deadcode     # informational dead code scan
make dev          # runs the agent, live-reloads with `entr` if installed
make clean
```

NVML (NVIDIA GPU) support is enabled by default for Linux builds. It is safe on
hosts without NVIDIA GPUs; the runtime collector only uses it when NVIDIA/NVML is
available. Use `NVML=false` only when you need to omit the glibc/NVML build tag.
Cross-compile with `OS=... ARCH=...`.

`amd64` builds default to `GOAMD64=v3` for x86-64-v3 CPUs. Use `GOAMD64=v2 make build` for a broader release baseline.

## Install

Releases publish Linux `amd64` v2 artifacts only: a `.deb` package and a
tarball. The `.deb` is the recommended Linux install because it includes the
binary, default config, and systemd unit.

### APT repository (Debian/Ubuntu)

```sh
curl -fsSL https://mordillosan.github.io/go-monitoring/install.sh | sudo sh
```

That adds the signed APT repository and installs the package; the service is
enabled and started automatically. New releases then arrive through regular
`sudo apt update && sudo apt upgrade`.

Prefer not to pipe a script into a shell? The equivalent manual steps:

```sh
sudo curl -fsSL https://mordillosan.github.io/go-monitoring/apt/gpg.key -o /etc/apt/keyrings/go-monitoring.asc
echo "deb [signed-by=/etc/apt/keyrings/go-monitoring.asc arch=amd64] https://mordillosan.github.io/go-monitoring/apt stable main" \
  | sudo tee /etc/apt/sources.list.d/go-monitoring.list
sudo apt update && sudo apt install go-monitoring
```

See [packaging/README.md](packaging/README.md) for how the repository is built
and the one-time maintainer setup.

### Manual .deb install

```sh
sudo apt install ./go-monitoring_<version>_amd64.deb
```

The package installs:

- `/usr/bin/go-monitoring`
- `/etc/go-monitoring/config.json`
- `/lib/systemd/system/go-monitoring.service`
- `/var/lib/go-monitoring` for `metrics.db`

Change the listen port or collection interval through the config CLI, then reload
the service:

```sh
sudo go-monitoring config --config /etc/go-monitoring/config.json --listen :9000
sudo go-monitoring config --config /etc/go-monitoring/config.json --collector-interval 30s
sudo systemctl reload go-monitoring.service
```

## Run

```sh
./go-monitoring                       # show CLI help
./go-monitoring run                   # listens on :45876 and collects every 15s by default
./go-monitoring run --listen :9000    # custom address/port
./go-monitoring run --history cpu,mem # store history only for selected plugins
./go-monitoring health                # exit 0 if the latest tick is fresh
./go-monitoring status                # query a running local agent
./go-monitoring --version
```

Dash-prefixed command aliases are also accepted: `./go-monitoring -run` and
`./go-monitoring -config`.

Configure the agent with a JSON file:

```sh
./go-monitoring config                 # interactive menu with arrow keys
./go-monitoring config --init
./go-monitoring config path
./go-monitoring config validate
./go-monitoring config --collector-interval 30s --api-cache containers=10s
./go-monitoring config --print
./go-monitoring run --config ~/.config/go-monitoring/config.json
```

The config file defaults to `$CONFIG_FILE` when set. Root runs use
`/etc/go-monitoring/config.json`; non-root runs use
`$XDG_CONFIG_HOME/go-monitoring/config.json` or `~/.config/go-monitoring/config.json`.
If the file is absent, `run` creates it from the effective startup config.
Precedence is built-in defaults, config file, legacy environment variables,
and explicit CLI flags. If the config file cannot be created, the agent
continues with the effective config in memory.

The config file is versioned with `"version": 1`. Send `SIGHUP` to a running
agent to reload config-backed collector interval, live API cache TTLs, and
history plugin settings without a full restart.

Database operations:

```sh
go-monitoring db path                 # print the metrics.db path
go-monitoring db check                # verify SQLite integrity and schema
go-monitoring db maintain             # rollups, retention, vacuum, integrity check
go-monitoring db repair               # recreate only if the DB is corrupt/unreadable
go-monitoring db reset --force        # move aside DB files and create an empty DB
```

`db repair` and `db reset --force` preserve old files with `.repair-*` or
`.reset-*` suffixes. Stop the service before reset or repair:

```sh
sudo systemctl stop go-monitoring.service
sudo go-monitoring db repair
sudo systemctl start go-monitoring.service
```

Environment variables:

- `CONFIG_FILE` — config file path
- `LISTEN` / `PORT` — fallback listen address if `--listen` is not provided
- `HISTORY` — comma-separated history plugin allowlist, or `all` / `none` (`cpu,mem,diskio,network,containers` by default)
- `MEM_CALC` — memory calculation formula
- `DISK_USAGE_CACHE` — cache duration for disk-usage polling (e.g. `15m`) to avoid waking sleeping disks
- `LOG_LEVEL` — set to `debug` for verbose logs
- `HTTP_LOG` / `REQUEST_LOG` — set to `false` to disable HTTP request logs (`true` by default)
- `API_CACHE_DEFAULT` — default TTL for live current API response caches (duration, e.g. `2s`; `0s` disables response caching)
- `API_CACHE_EXPENSIVE` — TTL for expensive live current plugins (`containers`, `systemd`, `processes`, `programs`, `connections`, `smart`)
- `API_CACHE_<PLUGIN>` — per-plugin live current TTL override, for example `API_CACHE_PROCESSES=10s`, `API_CACHE_CONTAINERS=5s`, `API_CACHE_SMART=30s`
- `API_CACHE_SYSTEM_SUMMARY` — TTL for `GET /api/v1/system/summary`
- `GPU_COLLECTOR` — comma-separated collector priority override (for example `nvml`, `amd_sysfs`, `intel_gpu_top`, `nvtop`)
- `SKIP_GPU` — set to `true` to disable GPU monitoring entirely

GPU auto-selection defaults to `tegrastats` on Jetson, `nvidia-smi` with NVML fallback for NVIDIA when the runtime exposes NVML, `amd_sysfs` with `rocm-smi` fallback for AMD, `intel_gpu_top` for Intel, and `nvtop` only as a last resort. Apple Silicon collectors remain opt-in via `GPU_COLLECTOR`.

At startup the agent logs which GPU collectors were discovered and which ones were selected.

## Systemd

The Debian package installs
[packaging/systemd/go-monitoring.service](packaging/systemd/go-monitoring.service).
The source tree also keeps a manual sample at
[contrib/systemd/go-monitoring.service](contrib/systemd/go-monitoring.service).
Both pin `CONFIG_FILE=/etc/go-monitoring/config.json` and
`DATA_DIR=/var/lib/go-monitoring` and include conservative hardening. They
intentionally avoid stronger sandboxing such as `PrivateDevices`, `ProtectProc`,
`PrivateNetwork`, and a tight capability bounding set because host metrics,
Docker/DBus, SMART, and GPU collectors may need host `/proc`, `/sys`, sockets,
and devices.

## HTTP API

Base URL: `http://<listen>`

- `GET /healthz` — liveness / freshness
- `GET /api/v1/meta` — agent metadata, effective config metadata, and collector interval
- `GET /api/v1/plugins` — plugin metadata and mounted routes
- `GET /api/v1/all` — current snapshots keyed by plugin name
- `GET /api/v1/{plugin}` — current plugin snapshot
- `GET /api/v1/{plugin}/history` — plugin history when enabled (`resolution`, `from`, `to`, `limit`)
- `POST /api/v1/{plugin}/refresh` — force a plugin refresh when supported

Current endpoints are served from live providers with small configurable
in-memory TTL caches. History endpoints are served from SQLite.

Plugins: `cpu`, `mem`, `swap`, `load`, `diskio`, `fs`, `network`, `gpu`, `sensors`, `containers`, `systemd`, `processes`, `programs`, `connections`, `irq`, `smart`.

Only history-enabled plugins mount `/{plugin}/history`. By default this is `cpu`, `mem`, `diskio`, `network`, and `containers`; use `--history` or `HISTORY` to change it.

## License

GPL-2.0-only — see [LICENSE](LICENSE). Upstream Beszel-originated portions retain the MIT notice recorded in [NOTICE](NOTICE).
