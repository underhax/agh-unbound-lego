FROM adguard/adguardhome:v0.107.80@sha256:e2e1b48c832f7705be915873f95de0d5114de573736441d7f2923a8a6293027c AS agh
FROM goacme/lego:v5.6.0@sha256:6f1583fc026a455ea46a7a5858c576105bb69c2e2de69db391cab7f14750350c AS lego
FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS unbound-donor

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates unbound

FROM unbound-donor AS runtime-libs

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

RUN mkdir -p /out && \
    find -L /lib* /usr/lib* -type f 2>/dev/null | \
      while read -r f; do \
        dir="/out$(dirname "$f")" && \
        mkdir -p "$dir" && \
        cp -L "$f" "$dir/"; \
      done

FROM golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder

SHELL ["/bin/ash", "-eo", "pipefail", "-c"]

ARG TARGETARCH

WORKDIR /src
COPY go.mod ./
COPY . .

RUN ARCH="${TARGETARCH:-$(uname -m)}" \
    && case "${ARCH}" in x86_64) ARCH="amd64" ;; aarch64) ARCH="arm64" ;; esac \
    && CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} go build -ldflags="-s -w" -trimpath -o /out/supervisor cmd/supervisor/main.go \
    && mkdir -p /out/opt/adguardhome/work \
                /out/opt/adguardhome/conf \
                /out/opt/unbound \
                /out/opt/lego \
                /out/etc/unbound \
                /out/etc/ssl/certs \
                /out/tmp \
                /out/run \
    && cp /etc/ssl/certs/ca-certificates.crt /out/etc/ssl/certs/ca-certificates.crt \
    && cp build/unbound.conf.default /out/etc/unbound/unbound.conf.default \
    && chmod 0700 /out/opt/adguardhome/work \
                  /out/opt/adguardhome/conf \
                  /out/opt/unbound \
                  /out/opt/lego \
    && chmod 1777 /out/tmp /out/run \
    && chmod 0755 /out/etc /out/etc/unbound /out/etc/ssl /out/etc/ssl/certs \
    && chmod 0444 /out/etc/ssl/certs/ca-certificates.crt \
    && chmod 0444 /out/etc/unbound/unbound.conf.default

FROM scratch

ENV PATH=/usr/local/bin:/usr/local/sbin:/usr/bin:/usr/sbin:/bin:/sbin

COPY --from=runtime-libs /out/ /
COPY --from=builder /out/etc /etc
COPY --from=builder /out/opt /opt
COPY --from=builder /out/tmp /tmp
COPY --from=builder /out/run /run

COPY --from=builder --chmod=0555 /out/supervisor /usr/local/bin/supervisor
COPY --from=agh --chmod=0555 /opt/adguardhome/AdGuardHome /opt/adguardhome/AdGuardHome
COPY --from=lego --chmod=0555 /lego /usr/local/bin/lego
COPY --from=unbound-donor --chmod=0555 /usr/sbin/unbound /usr/local/bin/unbound
COPY --from=unbound-donor --chmod=0555 /usr/sbin/unbound-anchor /usr/local/bin/unbound-anchor

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
