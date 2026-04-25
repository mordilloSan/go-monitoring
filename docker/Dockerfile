# syntax=docker/dockerfile:1

FROM golang:1.26.2-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG GOAMD64=v3
ARG VERSION=container
ARG COMMIT_SHA=
ARG BUILD_TIME=

RUN if [ "${TARGETARCH}" = "amd64" ]; then export GOAMD64="${GOAMD64}"; fi; \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
	-buildvcs=false \
	-o /out/go-monitoring \
	-ldflags "-w -s \
		-X github.com/mordilloSan/go-monitoring/internal/version.Version=${VERSION} \
		-X github.com/mordilloSan/go-monitoring/internal/version.CommitSHA=${COMMIT_SHA} \
		-X github.com/mordilloSan/go-monitoring/internal/version.BuildTime=${BUILD_TIME}" \
	./cmd/go-monitoring

FROM alpine:3.23

RUN apk add --no-cache ca-certificates smartmontools

COPY --from=build /out/go-monitoring /usr/local/bin/go-monitoring

ENV DATA_DIR=/var/lib/go-monitoring \
	LISTEN=:45876 \
	HISTORY=cpu,mem,diskio,network,containers

RUN mkdir -p /var/lib/go-monitoring

EXPOSE 45876
VOLUME ["/var/lib/go-monitoring"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
	CMD ["/usr/local/bin/go-monitoring", "health"]

ENTRYPOINT ["/usr/local/bin/go-monitoring"]
