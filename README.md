# agh-unbound-lego

This project provides a highly secure, all-in-one DNS resolution stack combining [**AdGuard Home**](https://github.com/AdguardTeam/AdGuardHome) (ad blocking and filtering), [**Unbound**](https://github.com/NLnetLabs/unbound) (recursive, validating, and caching DNS resolver), and [**Lego**](https://github.com/go-acme/lego) (ACME client for automated Let's Encrypt TLS certificates).

[![CI](https://github.com/webstudiobond/agh-unbound-lego/actions/workflows/ci.yml/badge.svg)](https://github.com/webstudiobond/agh-unbound-lego/actions/workflows/ci.yml)
[![GitHub last commit](https://img.shields.io/github/last-commit/webstudiobond/agh-unbound-lego)](https://github.com/webstudiobond/agh-unbound-lego/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/webstudiobond/agh-unbound-lego)](https://github.com/webstudiobond/agh-unbound-lego/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/webstudiobond/agh-unbound-lego)](https://github.com/webstudiobond/agh-unbound-lego)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

## About this Container

The primary motivation for this unified container is to allow AdGuard Home to reliably use `127.0.0.1` as its upstream Unbound DNS server, bypassing the limitations and unpredictable IP assignments associated with running them in separate containers.

### Architecture & Logic
* **AdGuard Home** acts as the front-facing DNS server and administrative interface, handling client requests (DoT, DoQ, DoH, and plain DNS) and applying rules.
* **Unbound** runs locally within the same network namespace. AdGuard Home forwards safe queries directly to Unbound (`127.0.0.1:5053`), which recursively resolves them without relying on third-party upstream providers (e.g., Google or Cloudflare), maximizing privacy.
* **Lego** automates the acquisition and renewal of wildcard TLS certificates via the Cloudflare DNS-01 challenge, ensuring secure DoT/DoH/DoQ connections without exposing port 80 to the public internet.

### Security Posture
This container is hardened by design, adhering strictly to the principle of least privilege:
* **Non-Root Execution:** Processes run under a dedicated unprivileged user/group (`uid: 2000`, `gid: 2000`).
* **Read-Only Filesystem:** The container's root filesystem is mounted as read_only: true to prevent the execution of malicious payloads if compromised.
* **Volatile Storage:** Temporary files and sockets are constrained to tmpfs mounts with strict noexec and nosuid flags.
* **Privilege Dropping:** All kernel capabilities are dropped (`cap_drop: ALL`), explicitly allowing only `NET_BIND_SERVICE` to bind privileged DNS ports.
* **No New Privileges:** Security options (`no-new-privileges:true`) prevent child processes from escalating privileges.
* **Secret Management:** Sensitive data, such as Cloudflare API tokens and account emails, are injected securely via Docker Secrets rather than environment variables.

### Container Configuration Details
**Exposed Ports:**
* `127.0.0.1:8002:80/tcp`, `127.0.0.1:6443:443/tcp|udp`: AdGuard Home Admin UI and DNS-over-HTTPS (DoH).
* `853:853/tcp|udp`: DNS-over-TLS (DoT) and DNS-over-QUIC (DoQ) exposed publicly.
* Standard port 53 DHCP ports are commented out by default but available for legacy clients if needed.
* `5443:5443/tcp|udp`: DNSCrypt ports  are commented out by default but available.
* `127.0.0.1:6060:6060/tcp`: Pprof endpoints commented out by default but available for memory/CPU profiling during active troubleshooting.

**Mounted Volumes:**
* `./data/agh/work` -> `/opt/adguardhome/work/data` (Persistent AdGuard Home database and statistics)
* `./data/agh/conf` -> `/opt/adguardhome/conf` (AdGuard Home configuration file)
* `./data/unbound` -> `/opt/unbound` (Unbound configuration and root trust anchors)
* `./data/lego` -> `/opt/lego` (Generated TLS certificates)

**Networking & Resource Limits:**
* Runs on a dedicated bridge network (`10.101.100.0/24`) with a static container IP (`10.101.100.2`).
* Hard-capped at 0.5 CPU cores and 512m RAM by default to prevent host resource exhaustion.

## Getting Started

**IMPORTANT**: Placeholder values such as the user (`dns`), domain (`dns.example.tld`), password (`P4$$w@rD`), email (`dns@example.tld`), and SSH connection string (`user@remote_ip`) are used throughout this guide. You must replace them with your actual configuration details.

### 1. Enable QUIC Support on the Host
To ensure optimal performance for DNS-over-QUIC, increase the UDP receive/send buffer sizes on your host system:

```bash
cat << EOF >> /etc/sysctl.conf
net.core.rmem_max = 7500000
net.core.wmem_max = 7500000
EOF
sysctl -p
```

### 2. Configure the Environment (HestiaCP Example)
If you are using [HestiaCP](https://hestiacp.com), create a dedicated user and web domain using the CLI:

```bash
v-add-user dns P4$$w@rD dns@example.tld
v-add-domain dns dns.example.tld
```

### 3. Create the Directory Structure
Set up the required directories for persistent storage and secrets:

```bash
mkdir -p /home/dns/{secrets,data/{unbound,lego,agh/{conf,work}}}
```

### 4. Initial Bootstrap Run
Before running the full stack, start a temporary interactive container to complete the AdGuard Home Setup Wizard:

```bash
docker run --rm -it \
  --name agh-bootstrap \
  -u root \
  -v $(pwd)/data/agh/work:/opt/adguardhome/work/data \
  -v $(pwd)/data/agh/conf:/opt/adguardhome/conf \
  -p 3000:3000/tcp \
  -p 127.0.0.1:6553:53/udp \
  -e LEGO_ENABLE=false \
  -e LOG_LEVEL=debug \
  agh-unbound-lego:latest
```

*If you are configuring a remote headless server, use an SSH tunnel to access the UI safely:*

```bash
# Connect to a remote host on a custom SSH port (e.g., 2222) with local port forwarding
ssh -p 2222 -L 3000:127.0.0.1:3000 user@remote_ip
```

Open [http://127.0.0.1:3000](http://127.0.0.1:3000) in your local browser and complete the wizard:
* Set up an administrative username and a strong password.
* Navigate to `Settings` -> `DNS Settings`. In the `Upstream DNS` servers field, enter: `127.0.0.1:5053` and click `Apply`.
* Navigate to `Settings` -> `General Settings` -> `Logs configuration` & `Statistics configuration`. Add `dns.healthcheck.arpa` to the `Ignored domains` list and click `Save`.
* Configure any other desired settings, then press `Ctrl+C` in your terminal to stop the bootstrap container.

### 5. Set Directory Permissions
Ensure the application user has the correct ownership of the data directories (matching the container's internal `UID/GID 2000`). This step is mandatory.

```bash
chown -R 2000:2000 /home/dns/data
```

### 6. Prerequisites for Automated TLS (Lego)
If you intend to use Let's Encrypt for DoT/DoQ/DoH:

**Cloudflare DNS Configuration:**
* In your [Cloudflare Dashboard](https://dash.cloudflare.com), create the following DNS records (ensure proxying is disabled / "`DNS Only`"):
  * `A` record for `dns.example.tld` pointing to your server's IPv4 address.
  * `AAAA` record for `dns.example.tld` pointing to your server's IPv6 address (if applicable).
  * `CNAME` record for `*.dns.example.tld` pointing to `dns.example.tld`.
* Generate an [API Token](https://dash.cloudflare.com/profile/api-tokens) with `Zone:DNS:Edit` permissions at Cloudflare API Tokens.

**Configure Secrets:**
Use an editor like `nano` to populate the secrets files to avoid leaking sensitive data in your shell history.

```bash
nano /home/dns/secrets/acme_email
nano /home/dns/secrets/cf_dns_api_token
```

Set strict permissions so only the owner can read them:

```bash
chmod 400 /home/dns/secrets/acme_email /home/dns/secrets/cf_dns_api_token
chown -R 2000:2000 /home/dns/secrets
```

### 7. Docker Compose Setup
Download the [Docker Compose file](https://raw.githubusercontent.com/webstudiobond/agh-unbound-lego/main/docker-compose.yaml):

```bash
curl --proto '=https' -sSLf -o /home/dns/docker-compose.yaml https://raw.githubusercontent.com/webstudiobond/agh-unbound-lego/main/docker-compose.yaml
```

Edit the downloaded `docker-compose.yaml` to enable Lego, uncomment both the service-level and top-level secrets blocks, and configure your actual domain for certificate generation:

```bash
nano /home/dns/docker-compose.yaml
```

* Change `LEGO_ENABLE=false` to `LEGO_ENABLE=true`
* Set `ACME_DOMAIN=` to your actual domain (e.g., `ACME_DOMAIN=dns.example.tld`)
* Uncomment the secrets blocks at the bottom of the file if Lego is enabled.

```yaml
    secrets:
      - source: acme_email
      - source: cf_dns_api_token

secrets:
  acme_email:
    file: ./secrets/acme_email
  cf_dns_api_token:
    file: ./secrets/cf_dns_api_token
```

### 8. Scaling & Optimization (Optional)
The default image is highly optimized for lightweight home usage (1 thread, minimal caching). For high-load environments (100+ clients), scale the service by modifying `/home/dns/data/unbound/unbound.conf`:
* `num-threads`: Set to the number of allocated CPU cores (e.g., `2`).
* `msg-cache-size`: Increase the DNS message cache (e.g., `32m`).
* `rrset-cache-size`: Must be exactly 2x the msg-cache-size (e.g., `64m`).

*Note: If you increase these caching values, you must proportionally raise the cpus and memory limits in your `docker-compose.yaml`.*

```yaml
    deploy:
      resources:
        limits:
          cpus: "1.0"
          memory: 1024m
        reservations:
          cpus: "0.1"
          memory: 256m
```

### 9. Start the Service

```bash
docker compose -f /home/dns/docker-compose.yaml up -d
```

Refer to the [AdGuard Home Wiki](https://github.com/AdguardTeam/AdGuardHome/wiki) for configuration.

### 10. Web Server Proxy Setup (HestiaCP)
Download the custom Nginx templates for AdGuard Home:

```bash
curl --proto '=https' -sSLf -o /usr/local/hestia/data/templates/web/nginx/php-fpm/sb_agh.stpl https://raw.githubusercontent.com/webstudiobond/agh-unbound-lego/main/usr/local/hestia/data/templates/web/nginx/php-fpm/sb_agh.stpl
```

```bash
curl --proto '=https' -sSLf -o /usr/local/hestia/data/templates/web/nginx/php-fpm/sb_agh.tpl https://raw.githubusercontent.com/webstudiobond/agh-unbound-lego/main/usr/local/hestia/data/templates/web/nginx/php-fpm/sb_agh.tpl
```

Edit `sb_agh.tpl` and replace `agh-secret-path` with your own unique path.

Apply the template to your domain:

```bash
v-change-web-domain-tpl dns dns.example.tld sb_agh
```

## Building the Image Locally (Optional)
To compile the Docker image yourself from the source repository:

```bash
cd /home/dns/

git clone https://github.com/webstudiobond/agh-unbound-lego.git

cd /home/dns/agh-unbound-lego

docker build --no-cache --progress=plain --tag agh-unbound-lego:latest .

docker compose -f /home/dns/agh-unbound-lego/docker-compose.dev.yaml up -d
```

## Useful Networking Tips

**Sharing the DNS resolver with other Docker containers:**
To allow containers from separate Docker Compose stacks to use this DNS server, connect them to the same external network and hardcode the DNS IP:

```yaml
dns:
  - 10.101.100.2
networks:
  - agh_dns

networks:
  agh_dns:
    external: true
    name: adguardhome_dns # Must match the actual network name created by the AGH compose stack
```

**Global Docker DNS configuration:**
Alternatively, to force all containers on the host machine to use this DNS server by default, update your `/etc/docker/daemon.json`:

```json
{
  "dns": ["10.101.100.2", "1.1.1.1", "8.8.8.8"]
}
```

## Basic Operations

```bash
# Create and start the stack in the background
docker compose -f /home/dns/docker-compose.yaml up -d

# Stop and remove the containers
docker compose -f /home/dns/docker-compose.yaml down
```
