# syntax=docker/dockerfile:1
FROM adguard/adguardhome:v0.107.73@sha256:7fbf01d73ecb7a32d2d9e6cef8bf88e64bd787889ca80a1e8bce30cd4c084442 AS agh
FROM goacme/lego:v4.33.0@sha256:fc6df3aad84814e983d7f6111c81b3d9f2bf626bfd0b644f5a7ef3cb7eda4cc6 AS lego
FROM golang:1.26.1-alpine3.23@sha256:2389ebfa5b7f43eeafbd6be0c3700cc46690ef842ad962f6c5bd6be49ed82039 AS builder

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
COPY . .

# Compile static Go supervisor
RUN ARCH="${TARGETARCH:-$(uname -m)}" \
    && case "${ARCH}" in x86_64) ARCH="amd64" ;; aarch64) ARCH="arm64" ;; esac \
    && CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} go build -ldflags="-s -w" -o /build/supervisor cmd/supervisor/main.go

FROM alpine:3.23.3@sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

WORKDIR /opt

# Extract pre-compiled binaries from official images (Supply Chain Security validation handled by Dependabot)
COPY --from=agh /opt/adguardhome/AdGuardHome /opt/adguardhome/AdGuardHome
COPY --from=lego /lego /usr/local/bin/lego
COPY --from=builder /build/supervisor /usr/local/bin/supervisor

# Single unprivileged user manages the supervisor and child processes.
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates unbound \
    && addgroup -S -g 2000 appgroup \
    && adduser -S -D -H -u 2000 -G appgroup appuser \
    && mkdir -p /opt/adguardhome/work /opt/adguardhome/conf /opt/lego /opt/unbound /etc/unbound \
    && chown -R appuser:appgroup /opt/adguardhome /opt/lego /opt/unbound /etc/unbound

COPY build/unbound.conf.default /etc/unbound/unbound.conf.default
RUN chown appuser:appgroup /etc/unbound/unbound.conf.default

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
