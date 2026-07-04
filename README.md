[![License](https://img.shields.io/github/license/mordilloSan/go-monitoring)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/mordilloSan/go-monitoring)](go.mod)
[![CodeQL](https://github.com/mordilloSan/go-monitoring/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/mordilloSan/go-monitoring/actions/workflows/codeql.yml)

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

- [main.go](main.go) — entrypoint (`main` package); only place that calls `os.Exit`
- [cmd/](cmd/) — CLI commands, flag parsing, and the interactive config menu (`cmd` package)
- [internal/app/](internal/app/) — app lifecycle, collection orchestration, and remaining local samplers/managers
- [internal/api/http/](internal/api/http/) — REST API routes, handlers, request logging, and query parsing
- [internal/api/model/](internal/api/model/) — REST API response contracts
- [internal/integration/docker/](internal/integration/docker/) — Docker/Podman integration
- [internal/integration/docker/dockerapi/](internal/integration/docker/dockerapi/) — Docker Engine API wire DTOs
- [internal/domain/](internal/domain/) — shared domain data types (system, container, smart, systemd)
- [internal/logging/](internal/logging/) — slog setup: native journald handler under systemd, text on stderr otherwise
- [internal/store/](internal/store/) — SQLite persistence, history, and rollups
- [internal/health/](internal/health/) — freshness check used by `go-monitoring health`
- [internal/version/](internal/version/) — version/app metadata
- [internal/common/](internal/common/), [internal/utils/](internal/utils/), [internal/deltatracker/](internal/deltatracker/) — shared helpers

## Build

Requires Go (see [go.mod](go.mod) for the pinned toolchain).

```sh
make build        # produces ./go-monitoring
sudo make install # installs to /usr/local/bin (override with PREFIX/DESTDIR)
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

Change listeners or collection interval through the config CLI, then reload the
service. Bare ports are localhost-only; use an explicit host if you need a
different bind address:

```sh
sudo go-monitoring config --config /etc/go-monitoring/config.json \
  --listener name=metrics,address=127.0.0.1:9000,apis=metrics \
  --listener name=control,address=unix:/run/go-monitoring/agent.sock,apis=commands
sudo go-monitoring config --config /etc/go-monitoring/config.json --collector-interval 30s
sudo systemctl reload go-monitoring.service
```

## Run

```sh
./go-monitoring                       # interactive menu on a terminal; CLI help otherwise
./go-monitoring run                   # listens on 127.0.0.1:45876 and collects every 15s by default
./go-monitoring run --listener name=metrics,address=127.0.0.1:9000,apis=metrics
./go-monitoring run --listener name=metrics,address=:9000,apis=metrics
./go-monitoring run --listener name=control,address=unix:/run/go-monitoring/agent.sock,apis=commands
./go-monitoring run --listener name=metrics,address=127.0.0.1:45876,apis=metrics --listener name=control,address=unix:/run/go-monitoring/agent.sock,apis=commands
./go-monitoring run --history cpu,mem # store history only for selected plugins
./go-monitoring health                # exit 0 if the latest tick is fresh
./go-monitoring status                # query a running local agent (TCP or unix socket)
./go-monitoring --version
```

Started with no arguments on a terminal, the CLI opens an interactive menu
(also available as `go-monitoring menu`) covering the agent, status, the
config editor, and database operations. Listener addresses take three forms: a
bare port (localhost-only), `host:port`, and `unix:/path` (or a bare absolute
path) for a unix socket restricted to the agent's user and group. Use an empty
`listeners` array to disable API listeners entirely while the collector keeps
recording history. `status` reaches the agent over TCP or the unix socket
automatically; with the API disabled, use `go-monitoring health` for the
file-based liveness check.

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
Precedence is built-in defaults, config file, supported environment variables,
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
If you built from source and installed the binary to `/usr/local/bin`, copy
that unit and change `ExecStart` accordingly. The unit runs as root, pins
`CONFIG_FILE=/etc/go-monitoring/config.json` and
`DATA_DIR=/var/lib/go-monitoring`, and includes conservative hardening. Its
`RuntimeDirectory=go-monitoring` provides `/run/go-monitoring` for the default
command unix socket listener (`"address": "unix:/run/go-monitoring/agent.sock"`). It
intentionally avoids stronger sandboxing such as `PrivateDevices`, `ProtectProc`,
`PrivateNetwork`, and a tight capability bounding set because host metrics,
Docker/DBus, SMART, and GPU collectors may need host `/proc`, `/sys`, sockets,
and devices.

## HTTP API

The agent can expose more than one listener at the same time. Each listener
chooses which API families it serves:

- `metrics` — read-only metric, collection, history, metadata, and benchmark endpoints
- `commands` — JSON command endpoint used for status, config, SMART refresh, and DB maintenance commands

Default listeners:

```json
{
  "listeners": [
    {
      "name": "metrics",
      "address": "127.0.0.1:45876",
      "apis": ["metrics"]
    },
    {
      "name": "control",
      "address": "unix:/run/go-monitoring/agent.sock",
      "apis": ["commands"]
    }
  ]
}
```

`GET /healthz` is mounted on every listener. Metrics routes are only mounted on
listeners that include `metrics`; command routes are only mounted on listeners
that include `commands`.

There is no authentication layer in the agent. Keep command listeners on a unix
socket or loopback TCP unless the host network is trusted.

TCP example:

```sh
curl http://127.0.0.1:45876/healthz
curl http://127.0.0.1:45876/api/v1/meta
```

Unix socket example:

```sh
curl --unix-socket /run/go-monitoring/agent.sock http://localhost/healthz
curl --unix-socket /run/go-monitoring/agent.sock \
  -H 'Content-Type: application/json' \
  -d '{"command":"status.get"}' \
  http://localhost/api/v1/command
```

### Normal API

These endpoints are useful for liveness, metadata, and API discovery.

`GET /healthz`

Returns collection freshness:

```json
{
  "healthy": true,
  "last_updated": "2026-07-04T12:00:00Z",
  "age_seconds": 1.2
}
```

Returns HTTP `200` when fresh and HTTP `503` when stale or unavailable.

`GET /api/v1/meta`

Returns agent, database, listener, config, and retention metadata:

```json
{
  "version": "v1.2.0",
  "data_dir": "/var/lib/go-monitoring",
  "db_path": "/var/lib/go-monitoring/metrics.db",
  "listeners": [
    {
      "name": "metrics",
      "address": "127.0.0.1:45876",
      "effective_address": "127.0.0.1:45876",
      "apis": ["metrics"],
      "active": true
    }
  ],
  "collector_interval": "15s",
  "smart_refresh_interval": "",
  "config": {
    "path": "/etc/go-monitoring/config.json",
    "source": "loaded",
    "version": 1,
    "collector_interval": "15s",
    "history_plugins": ["cpu", "mem", "diskio", "network", "containers"],
    "cache_ttl": {
      "cpu": "2s",
      "containers": "5s"
    }
  },
  "retention": {
    "1m": "1h",
    "10m": "24h",
    "20m": "7d",
    "120m": "30d"
  }
}
```

`GET /api/v1/plugins`

Returns available metric plugins, whether each has history enabled, whether it
supports refresh, and the routes currently mounted for it.

`GET /api/v1/benchmark`

Runs the mounted read endpoints internally and returns status, duration, and
response size per endpoint. This is diagnostic; it does not change persisted
data.

### Normal Collection API

These are the read-only metrics endpoints served by `metrics` listeners.
Current endpoints are served from live collectors with small configurable
in-memory TTL caches. History endpoints are served from SQLite.

Plugins:

`cpu`, `mem`, `swap`, `load`, `diskio`, `fs`, `network`, `gpu`, `sensors`,
`containers`, `systemd`, `processes`, `programs`, `connections`, `irq`, `smart`

`GET /api/v1/all`

Returns the current snapshot for every plugin, keyed by plugin name. If some
plugins fail but at least one succeeds, the response includes an `errors`
object with per-plugin public error messages.

`GET /api/v1/system/summary`

Returns a compact host summary for frequent polling.

`GET /api/v1/{plugin}`

Returns the current snapshot for one plugin:

```sh
curl http://127.0.0.1:45876/api/v1/cpu
curl http://127.0.0.1:45876/api/v1/network
curl http://127.0.0.1:45876/api/v1/containers
```

Most current plugin responses have this shape:

```json
{
  "captured_at": 1783166400000,
  "data": {}
}
```

Item plugins use `items` instead of `data`; process responses may also include
`count`.

`GET /api/v1/{plugin}/history`

Returns history for plugins that have history enabled.

Query parameters:

- `resolution` — rollup resolution, default `1m`
- `from` — inclusive unix milliseconds lower bound, default `0`
- `to` — inclusive unix milliseconds upper bound, default now
- `limit` — maximum rows, default `200`, maximum `1000`

Example:

```sh
curl 'http://127.0.0.1:45876/api/v1/cpu/history?resolution=1m&from=0&limit=100'
```

Response:

```json
{
  "resolution": "1m",
  "items": [
    {
      "captured_at": 1783166400000,
      "stats": {}
    }
  ]
}
```

Default history plugins are `cpu`, `mem`, `diskio`, `network`, and
`containers`. Change them with `history` in config, `--history`, or the config
API. Plugins without enabled history return HTTP `404` for history routes.

`POST /api/v1/{plugin}/refresh`

Refreshes a plugin when supported. Currently this is primarily useful for:

```sh
curl -X POST http://127.0.0.1:45876/api/v1/smart/refresh
```

The response is the refreshed current plugin payload.

### Config API

The config/control API is exposed through `POST /api/v1/command` on listeners
that include `commands`. Requests and responses are JSON.

Request:

```json
{
  "command": "config.get",
  "request_id": "optional-client-id",
  "params": {}
}
```

Response:

```json
{
  "ok": true,
  "command": "config.get",
  "request_id": "optional-client-id",
  "restart_required": false,
  "data": {}
}
```

Error response:

```json
{
  "ok": false,
  "command": "config.set",
  "request_id": "optional-client-id",
  "error": {
    "code": "invalid_config",
    "message": "listeners[0].apis cannot be empty"
  }
}
```

Commands:

- `commands.list` — returns the command names supported by this agent
- `status.get` — returns the same metadata shape as `/api/v1/meta`, using command transport
- `config.get` — loads and returns the on-disk config
- `config.set` — validates, saves, and live-applies supported config fields
- `config.reload` — reloads effective config and live-applies supported fields
- `smart.refresh` — refreshes SMART data immediately
- `db.check` — runs SQLite integrity check
- `db.maintain` — runs database maintenance and integrity check

`config.set` accepts these `params` fields:

```json
{
  "collector_interval": "30s",
  "history": "cpu,mem,diskio,network,containers",
  "cache_ttl": {
    "cpu": "2s",
    "containers": "5s",
    "smart": "30s"
  },
  "listeners": [
    {
      "name": "metrics",
      "address": "127.0.0.1:45876",
      "apis": ["metrics"]
    },
    {
      "name": "control",
      "address": "unix:/run/go-monitoring/agent.sock",
      "apis": ["commands"]
    }
  ],
  "allow_remote_commands": false
}
```

Live-applied without restart:

- `collector_interval`
- `history`
- `cache_ttl`

Saved but requiring agent restart:

- `listeners`

Command listeners on non-loopback TCP addresses are rejected unless
`allow_remote_commands` is set to `true`.

Examples:

```sh
curl --unix-socket /run/go-monitoring/agent.sock \
  -H 'Content-Type: application/json' \
  -d '{"command":"commands.list"}' \
  http://localhost/api/v1/command
```

```sh
curl --unix-socket /run/go-monitoring/agent.sock \
  -H 'Content-Type: application/json' \
  -d '{"command":"config.set","params":{"collector_interval":"30s","cache_ttl":{"containers":"10s"}}}' \
  http://localhost/api/v1/command
```

```sh
curl --unix-socket /run/go-monitoring/agent.sock \
  -H 'Content-Type: application/json' \
  -d '{"command":"db.check","request_id":"check-1"}' \
  http://localhost/api/v1/command
```

## License

GPL-2.0-only — see [LICENSE](LICENSE). Upstream Beszel-originated portions retain the MIT notice recorded in [NOTICE](NOTICE).
