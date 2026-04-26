# syntax=docker/dockerfile:1

FROM golang:1.26.2-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG GOAMD64=v3
ARG NVML=auto
ARG VERSION=container
ARG COMMIT_SHA=
ARG BUILD_TIME=

RUN tags=""; \
	if [ "${NVML}" = "true" ] || { [ "${NVML}" = "auto" ] && [ "${TARGETOS}" = "linux" ] && [ "${TARGETARCH}" = "amd64" ]; }; then tags="-tags=glibc"; fi; \
	if [ "${TARGETARCH}" = "amd64" ]; then export GOAMD64="${GOAMD64}"; fi; \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
	${tags} \
	-buildvcs=false \
	-o /out/go-monitoring \
	-ldflags "-w -s \
		-X github.com/mordilloSan/go-monitoring/internal/version.Version=${VERSION} \
		-X github.com/mordilloSan/go-monitoring/internal/version.CommitSHA=${COMMIT_SHA} \
		-X github.com/mordilloSan/go-monitoring/internal/version.BuildTime=${BUILD_TIME}" \
	./cmd/go-monitoring

FROM debian:bookworm-slim AS intel-tools

RUN apt-get update \
	&& apt-get install -y --no-install-recommends intel-gpu-tools \
	&& rm -rf /var/lib/apt/lists/*

FROM alpine:3.23

RUN apk add --no-cache ca-certificates smartmontools

COPY --from=build /out/go-monitoring /usr/local/bin/go-monitoring
COPY --from=intel-tools /usr/bin/intel_gpu_top /usr/bin/intel_gpu_top
COPY --from=intel-tools /lib64/ld-linux-x86-64.so.2 /lib64/ld-linux-x86-64.so.2
COPY --from=intel-tools /lib/x86_64-linux-gnu/libc.so.6 /lib/x86_64-linux-gnu/libm.so.6 /lib/x86_64-linux-gnu/libdl.so.2 /lib/x86_64-linux-gnu/libpthread.so.0 /usr/lib/
COPY --from=intel-tools /lib/x86_64-linux-gnu/libglib-2.0.so.0 /lib/x86_64-linux-gnu/libudev.so.1 /lib/x86_64-linux-gnu/libpcre2-8.so.0 /usr/lib/

ENV DATA_DIR=/var/lib/go-monitoring \
	CONFIG_FILE=/var/lib/go-monitoring/config.json \
	PORT=45876 \
	HISTORY=cpu,mem,diskio,network,containers

RUN ln -sf /usr/lib/libc.so.6 /lib/libc.so.6 \
	&& ln -sf /usr/lib/libm.so.6 /lib/libm.so.6 \
	&& ln -sf /usr/lib/libdl.so.2 /lib/libdl.so.2 \
	&& ln -sf /usr/lib/libpthread.so.0 /lib/libpthread.so.0 \
	&& mkdir -p /var/lib/go-monitoring

EXPOSE 45876
VOLUME ["/var/lib/go-monitoring"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
	CMD ["/usr/local/bin/go-monitoring", "health"]

ENTRYPOINT ["/usr/local/bin/go-monitoring"]
CMD ["run"]
