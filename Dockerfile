# syntax=docker/dockerfile:1
FROM adguard/adguardhome:v0.107.77@sha256:e6f2b8bcda06064ab055b44933a4f0e983c35558b9cdb8d2e7ab1efcee36d890 AS agh
FROM goacme/lego:v5.2.2@sha256:d621ec01f3ca272d259a62e3e00be901293c2901ba8fc0214fe0b72523c3c278 AS lego
FROM golang:1.26.5-alpine3.23@sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc AS builder

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

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

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
