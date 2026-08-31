# syntax=docker/dockerfile:1
FROM adguard/adguardhome:v0.107.79@sha256:aba9e3bf0613be3ba3755e1fc311b126e2c24bec25e18b6483894a88283074f0 AS agh
FROM goacme/lego:v5.4.1@sha256:ac04a7aaac0270ca2c32f1e79b157087d763e78c4473551c6e093070614536e2 AS lego
FROM golang:1.27.0-alpine3.24@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

ARG TARGETARCH

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN ARCH="${TARGETARCH:-$(uname -m)}" \
    && case "${ARCH}" in x86_64) ARCH="amd64" ;; aarch64) ARCH="arm64" ;; esac \
    && CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} go build -ldflags="-s -w" -o /build/supervisor cmd/supervisor/main.go

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

WORKDIR /opt

COPY --from=agh /opt/adguardhome/AdGuardHome /opt/adguardhome/AdGuardHome
COPY --from=lego /lego /usr/local/bin/lego
COPY --from=builder /build/supervisor /usr/local/bin/supervisor

RUN /opt/adguardhome/AdGuardHome --version \
    && /usr/local/bin/lego --version

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates unbound \
    && addgroup -S -g 2000 appgroup \
    && adduser -S -D -H -u 2000 -G appgroup appuser \
    && mkdir -p /opt/adguardhome/work /opt/adguardhome/conf /opt/lego /opt/unbound /etc/unbound \
    && chown -R appuser:appgroup /opt/adguardhome /opt/lego /opt/unbound /etc/unbound

COPY --chown=appuser:appgroup build/unbound.conf.default /etc/unbound/unbound.conf.default

USER 2000

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
