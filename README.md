[![Go Report Card](https://goreportcard.com/badge/github.com/mordilloSan/go-monitoring)](https://goreportcard.com/report/github.com/mordilloSan/go-monitoring)
[![License](https://img.shields.io/github/license/mordilloSan/go-monitoring)](LICENSE)
[![Go](https://img.shields.io/github/go-mod/go-version/mordilloSan/go-monitoring)](go.mod)
[![CodeQL](https://github.com/mordilloSan/go-monitoring/actions/workflows/github-code-scanning/codeql/badge.svg)](https://github.com/mordilloSan/go-monitoring/actions/workflows/github-code-scanning/codeql)

# go-monitoring

A standalone Go agent that collects local system metrics, persists them to an embedded SQLite store, and exposes them over an HTTP JSON API. Forked from Beszel.

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
make test
make golint       # gofmt + go mod tidy + modernize + golangci-lint
make dev          # runs the agent, live-reloads with `entr` if installed
make clean
```

NVML (NVIDIA GPU) support is enabled automatically on `linux/amd64` glibc hosts and in the Docker `linux/amd64` image. Override with `NVML=true` or `NVML=false`. Cross-compile with `OS=... ARCH=...`.

`amd64` builds default to `GOAMD64=v3` for x86-64-v3 CPUs. Use `GOAMD64=v1 make build` or `docker build --build-arg GOAMD64=v1 ...` for maximum amd64 compatibility.

## Run

```sh
./go-monitoring                       # show CLI help
./go-monitoring run                   # listens on :45876 and collects every 15s by default
./go-monitoring run --listen :9000    # custom address/port
./go-monitoring run --history cpu,mem # store history only for selected plugins
./go-monitoring health                # exit 0 if the latest tick is fresh
./go-monitoring --version
```

Dash-prefixed command aliases are also accepted: `./go-monitoring -run` and
`./go-monitoring -config`.

Configure the agent with a JSON file:

```sh
./go-monitoring config
./go-monitoring config --init
./go-monitoring config --collector-interval 30s --api-cache containers=10s
./go-monitoring config --print
./go-monitoring run --config ~/.config/go-monitoring/config.json
```

The config file defaults to `$CONFIG_FILE` when set. Root runs use
`/etc/go-monitoring/config.json`; non-root runs use
`$XDG_CONFIG_HOME/go-monitoring/config.json` or `~/.config/go-monitoring/config.json`.
If the file is absent, built-in defaults are used. Config values can be
overridden by the legacy environment variables below and then by explicit CLI
flags.

Environment variables:

- `CONFIG_FILE` — config file path. Docker uses `/var/lib/go-monitoring/config.json`.
- `LISTEN` / `PORT` — fallback listen address if `--listen` is not provided. Docker uses `PORT=45876` by default.
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

At startup the agent logs which GPU collectors were discovered and which ones were selected. In containers, detection only works against what the runtime actually exposes to the process: device nodes, libraries, capabilities, and helper binaries still need to be present in the container.

## Docker

Build and run the API locally with Compose:

```sh
make docker-up
```

The image starts `go-monitoring run` by default and also contains the CLI, so
config can be managed in the container:

```sh
docker compose -f docker/docker-compose.yml run --rm go-monitoring config --init
docker compose -f docker/docker-compose.yml run --rm go-monitoring config --collector-interval 30s --api-cache containers=10s
```

Compose stores the config file inside the existing `/var/lib/go-monitoring`
volume. You can also bind-mount a single config file to
`/var/lib/go-monitoring/config.json`; when that file is absent the container
loads built-in defaults.

`make docker-up` starts the Compose service in the foreground. Stop it with:

```sh
make docker-down
```

Then test the API:

```sh
curl http://localhost:45876/healthz
curl http://localhost:45876/api/v1/meta
curl http://localhost:45876/api/v1/plugins
curl http://localhost:45876/api/v1/all
```

The Docker setup avoids `--privileged` by using targeted host access:

- `network_mode: host` so host network interfaces are visible.
- `pid: host` so host processes and process-owned sockets are visible.
- `/var/run/docker.sock` read-only for container metrics.
- `/var/run/dbus/system_bus_socket` read-only for systemd state.
- `apparmor=unconfined` so the container can query host systemd over DBus.
- `systempaths=unconfined` so Docker does not mask host `/proc/interrupts` for IRQ counters.
- `CAP_SYS_RAWIO`, `CAP_SYS_ADMIN`, and explicit `/dev/...` device mappings for SMART data.
- `gpus: all` for NVIDIA when detected.
- `/dev/dri/...` device mappings for AMD and Intel when detected, plus `/dev/kfd` for AMD when present.
- `CAP_PERFMON` for Intel `intel_gpu_top`.

Compose cannot discover host devices dynamically, so `make docker-up` first writes a local `docker/docker-compose.override.yml` with discovered SMART devices and GPU access when available. You can inspect what will be used with:

```sh
make docker-smart-devices
```

If SMART detection chooses the wrong device, edit `docker/docker-compose.override.yml` or set `SMART_DEVICES` manually. Use controller devices such as `/dev/nvme0` or `/dev/sda`, not partitions such as `/dev/nvme0n1p2`.

The generated Compose override requests GPU access for NVIDIA, AMD, and Intel hosts. NVIDIA uses `gpus: all` and requires the NVIDIA Container Toolkit. AMD and Intel use DRM render/card device mappings; Intel also uses the `intel_gpu_top` binary installed in the image. Set `DOCKER_GPU=false make docker-up` to suppress GPU access, or `DOCKER_GPU=true make docker-up` to force it when auto-detection cannot see the host GPU.

## HTTP API

Base URL: `http://<listen>`

- `GET /healthz` — liveness / freshness
- `GET /api/v1/meta` — agent metadata and collector interval
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

GPL-2.0 — see [LICENSE](LICENSE).
