# syntax=docker/dockerfile:1

FROM golang:1.26.2-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=container
ARG COMMIT_SHA=
ARG BUILD_TIME=

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
	-buildvcs=false \
	-o /out/go-monitoring \
	-ldflags "-w -s \
		-X github.com/mordilloSan/go-monitoring/internal/version.Version=${VERSION} \
		-X github.com/mordilloSan/go-monitoring/internal/version.CommitSHA=${COMMIT_SHA} \
		-X github.com/mordilloSan/go-monitoring/internal/version.BuildTime=${BUILD_TIME}" \
	./cmd/go-monitoring

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl smartmontools \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /out/go-monitoring /usr/local/bin/go-monitoring

ENV DATA_DIR=/var/lib/go-monitoring \
	LISTEN=:45876

RUN mkdir -p /var/lib/go-monitoring

EXPOSE 45876
VOLUME ["/var/lib/go-monitoring"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
	CMD ["curl", "-fsS", "http://localhost:45876/healthz"]

ENTRYPOINT ["/usr/local/bin/go-monitoring"]
