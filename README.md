# go-monitoring

A standalone Go agent that collects local system metrics, persists them to an embedded SQLite store, and exposes them over an HTTP JSON API.

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
- [internal/agent/](internal/agent/) — agent core: collectors, HTTP server, store
- [internal/model/](internal/model/) — shared data types (system, container, smart, systemd)
- [internal/health/](internal/health/) — freshness check used by `go-monitoring health`
- [internal/version/](internal/version/) — version/app metadata
- [internal/common/](internal/common/), [internal/utils/](internal/utils/), [internal/deltatracker/](internal/deltatracker/) — shared helpers
- [internal/battery/](internal/battery/), [internal/zfs/](internal/zfs/) — platform-specific helpers

## Build

Requires Go (see [go.mod](go.mod) for the pinned toolchain).

```sh
make build        # produces ./go-monitoring
make test
make golint       # gofmt + go mod tidy + modernize + golangci-lint
make dev          # runs the agent, live-reloads with `entr` if installed
make clean
```

NVML (NVIDIA GPU) support is enabled automatically on `linux/amd64` glibc hosts. Override with `NVML=true` or `NVML=false`. Cross-compile with `OS=... ARCH=...`.

## Run

```sh
./go-monitoring                       # listens on :45876 by default
./go-monitoring --listen :9000        # custom address/port
./go-monitoring health                # exit 0 if the latest tick is fresh
./go-monitoring --version
```

Environment variables:

- `LISTEN` / `PORT` — fallback listen address if `--listen` is not provided
- `MEM_CALC` — memory calculation formula
- `DISK_USAGE_CACHE` — cache duration for disk-usage polling (e.g. `15m`) to avoid waking sleeping disks
- `LOG_LEVEL` — set to `debug` for verbose logs
- `HTTP_LOG` / `REQUEST_LOG` — set to `false` to disable HTTP request logs (`true` by default)
- `GPU_COLLECTOR` — comma-separated collector priority override (for example `nvml`, `amd_sysfs`, `intel_gpu_top`, `nvtop`)
- `SKIP_GPU` — set to `true` to disable GPU monitoring entirely

GPU auto-selection defaults to `tegrastats` on Jetson, `nvidia-smi` with NVML fallback for NVIDIA when the runtime exposes NVML, `amd_sysfs` with `rocm-smi` fallback for AMD, `intel_gpu_top` for Intel, and `nvtop` only as a last resort. Apple Silicon collectors remain opt-in via `GPU_COLLECTOR`.

At startup the agent logs which GPU collectors were discovered and which ones were selected. In containers, detection only works against what the runtime actually exposes to the process: device nodes, libraries, capabilities, and helper binaries still need to be present in the container.

## Docker

Build and run the API locally with Compose:

```sh
make docker-up
```

`make docker-up` starts the Compose service in the foreground. Stop it with:

```sh
make docker-down
```

Then test the API:

```sh
curl http://localhost:45876/healthz
curl http://localhost:45876/api/v1/meta
curl http://localhost:45876/api/v1/summary
```

The Docker setup avoids `--privileged` by using targeted host access:

- `network_mode: host` so host network interfaces are visible.
- `pid: host` so host processes and process-owned sockets are visible.
- `/var/run/docker.sock` read-only for container metrics.
- `/var/run/dbus/system_bus_socket` read-only for systemd state.
- `apparmor=unconfined` so the container can query host systemd over DBus.
- `systempaths=unconfined` so Docker does not mask host `/proc/interrupts` for IRQ counters.
- `CAP_SYS_RAWIO`, `CAP_SYS_ADMIN`, and explicit `/dev/...` device mappings for SMART data.

Compose cannot discover host devices dynamically, so `make docker-up` first writes a local `docker/docker-compose.override.yml` with discovered SMART devices. You can inspect what will be used with:

```sh
make docker-smart-devices
```

If SMART detection chooses the wrong device, edit `docker/docker-compose.override.yml` or set `SMART_DEVICES` manually. Use controller devices such as `/dev/nvme0` or `/dev/sda`, not partitions such as `/dev/nvme0n1p2`.

## HTTP API

Base URL: `http://<listen>`

- `GET /healthz` — liveness / freshness
- `GET /api/v1/meta` — agent metadata and collector interval
- `GET /api/v1/summary` — latest snapshot
- `GET /api/v1/history/system` — system history (`resolution`, `from`, `to`, `limit`)
- `GET /api/v1/history/containers` — container history
- `GET /api/v1/containers` — current container list
- `GET /api/v1/systemd` — systemd unit state
- `GET /api/v1/processlist` — current process list
- `GET /api/v1/processcount` — process counts by state
- `GET /api/v1/programlist` — process list grouped by program name
- `GET /api/v1/connections` — network connection and conntrack counts
- `GET /api/v1/irq` — IRQ counters
- `GET /api/v1/smart` — SMART data
- `POST /api/v1/smart/refresh` — force a SMART refresh

## License

MIT — see [LICENSE](LICENSE).
