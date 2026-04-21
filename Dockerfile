# syntax=docker/dockerfile:1
FROM adguard/adguardhome:v0.107.74@sha256:f29c58a91f79387cbbbb042e140814f58e830d457d44af03d662c8df43db9dea AS agh
FROM goacme/lego:v4.35.1@sha256:6eb6eace3f5ee9b6c3635d43be7144751d1ec7ed8a2fc6c73f195c6ad528ecb2 AS lego
FROM golang:1.26.2-alpine3.23@sha256:f85330846cde1e57ca9ec309382da3b8e6ae3ab943d2739500e08c86393a21b1 AS builder

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Compile static Go supervisor
RUN ARCH="${TARGETARCH:-$(uname -m)}" \
    && case "${ARCH}" in x86_64) ARCH="amd64" ;; aarch64) ARCH="arm64" ;; esac \
    && CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} go build -ldflags="-s -w" -o /build/supervisor cmd/supervisor/main.go

FROM alpine:3.23.4@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

WORKDIR /opt

# Extract pre-compiled binaries from official images (Supply Chain Security validation handled by Dependabot)
COPY --from=agh /opt/adguardhome/AdGuardHome /opt/adguardhome/AdGuardHome
COPY --from=lego /lego /usr/local/bin/lego
COPY --from=builder /build/supervisor /usr/local/bin/supervisor

# Validate copied binaries exist and are executable
RUN /opt/adguardhome/AdGuardHome --version \
    && /usr/local/bin/lego --version

# Single unprivileged user manages the supervisor and child processes.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates unbound \
    && addgroup -S -g 2000 appgroup \
    && adduser -S -D -H -u 2000 -G appgroup appuser \
    && mkdir -p /opt/adguardhome/work /opt/adguardhome/conf /opt/lego /opt/unbound /etc/unbound \
    && chown -R appuser:appgroup /opt/adguardhome /opt/lego /opt/unbound /etc/unbound

COPY --chown=appuser:appgroup build/unbound.conf.default /etc/unbound/unbound.conf.default

USER appuser

VOLUME ["/opt/adguardhome/conf", "/opt/adguardhome/work", "/opt/unbound", "/opt/lego"]

EXPOSE 53/tcp 53/udp \
       67/udp 68/tcp 68/udp \
       80/tcp 443/tcp 443/udp 3000/tcp \
       853/tcp 853/udp \
       5443/tcp 5443/udp \
       6060/tcp

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["/usr/local/bin/supervisor", "health"]

ENTRYPOINT ["/usr/local/bin/supervisor"]
