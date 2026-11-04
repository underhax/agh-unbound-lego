# adguardhome-unbound-lego

[![CI](https://github.com/underhax/adguardhome-unbound-lego/actions/workflows/ci.yml/badge.svg)](https://github.com/underhax/adguardhome-unbound-lego/actions/workflows/ci.yml)
[![GitHub last commit](https://img.shields.io/github/last-commit/underhax/adguardhome-unbound-lego)](https://github.com/underhax/adguardhome-unbound-lego/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/underhax/adguardhome-unbound-lego)](https://github.com/underhax/adguardhome-unbound-lego/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/underhax/adguardhome-unbound-lego)](https://github.com/underhax/adguardhome-unbound-lego)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Scratch-based container integrating [**AdGuard Home**](https://github.com/AdguardTeam/AdGuardHome) (DNS filtering), [**Unbound**](https://github.com/NLnetLabs/unbound) (recursive DNS resolver), and [**Lego**](https://github.com/go-acme/lego) (automated ACME DNS-01 certificate management).

## System Design

This stack provides an integrated DNS infrastructure built on the principle of least privilege:

* The scratch container contains only the custom Go supervisor, AdGuard Home, Unbound with required shared libraries, Lego, and CA root certificates, excluding shells, package managers, and system utilities.
* AdGuard Home functions as the primary network-wide DNS server, handling standard and encrypted protocols, enforcing filtering policies, and managing client traffic.
* Unbound serves as the local recursive resolver, processing upstream queries directly over the shared loopback interface without external network hops.
* Integrated Lego automates Let's Encrypt certificate acquisition and renewals via Cloudflare DNS-01 challenges without requiring open HTTP validation ports.
* All processes execute under a dedicated unprivileged user and group configured via environment variables.
* The runtime environment enforces a read-only root filesystem, hardened volume and tmpfs mounts, dropped Linux capabilities, strict resource quotas, and credential isolation through Docker Secrets.

---

<details>
<summary><strong>Directory Structure</strong></summary>

Production directory layout on the host (`/home/dns`):

```text
├── docker-compose.yaml                # Production deployment manifest
├── .env                               # Container environment configuration
├── data/                              # Persistent service storage
│   ├── adguardhome/                   # AdGuard Home data directory
│   │   ├── conf/                      # Active AdGuardHome.yaml configuration
│   │   └── work/                      # Databases, stats, filters, and query logs
│   ├── lego/                          # Let's Encrypt accounts and issued TLS certificates
│   └── unbound/                       # Recursive resolver configuration and root anchors
└── secrets/                           # Docker secrets directory
    ├── acme_domain                    # Domain name for certificate issuance
    ├── acme_email                     # Let's Encrypt account email
    └── cf_dns_api_token               # Cloudflare API token with DNS-01 edit permissions
```

</details>

---

<details>
<summary><strong>Deployment &amp; Setup</strong></summary>

### Prerequisites

* **Docker Engine & Compose:** Ensure Docker Engine and the Docker Compose plugin are installed on the host. Follow the official installation guide for [Ubuntu](https://docs.docker.com/engine/install/ubuntu/#install-using-the-repository).
* **External Gateway Network (Optional):** Required only when publishing AdGuard Home externally under a domain name via an [Angie Reverse Proxy](https://github.com/webstudiobond/angie-docker-compose), which automatically provisions the shared `frontend_gateway` network. If deploying standalone without the Angie stack, create it manually:

  ```bash
  docker network create --subnet=172.20.0.0/16 frontend_gateway
  ```

### 1. Create a Dedicated System User

Choose a dedicated, unprivileged numeric ID unique to this service on your host and verify that it is not already in use by another user or group:

```bash
APP_UID=<CHOSEN_UID>

getent passwd ${APP_UID}
getent group ${APP_UID}
```

```bash
sudo useradd -M -d /home/dns -s /usr/sbin/nologin -u ${APP_UID} -U dns
```

```bash
sudo mkdir -p /home/dns
sudo chown root:dns /home/dns
sudo chmod 0750 /home/dns
```

### 2. Prepare Directory Structure & Permissions

```bash
sudo mkdir -p /home/dns/{secrets,data/{adguardhome/{conf,work},lego,unbound}}

sudo chown root:dns /home/dns/secrets
sudo chmod 0750 /home/dns/secrets

sudo chown -R dns:dns /home/dns/data
sudo find /home/dns/data -type d -exec chmod 0700 {} +
```

### 3. Fetch Deployment Manifests

```bash
REPO="https://raw.githubusercontent.com/underhax/adguardhome-unbound-lego/main"

sudo curl -fsSL ${REPO}/docker-compose.yaml -o /home/dns/docker-compose.yaml
sudo curl -fsSL ${REPO}/.env.example -o /home/dns/.env

sudo chown root:root /home/dns/{docker-compose.yaml,.env}
sudo chmod 0600 /home/dns/{docker-compose.yaml,.env}
```

```bash
sudo sed -i "s/^APP_UID=.*/APP_UID=${APP_UID}/" /home/dns/.env
sudo sed -i "s/^APP_GID=.*/APP_GID=${APP_UID}/" /home/dns/.env
```

Review optional variables in `/home/dns/.env`:
* `HOST_BIND_IP`: Host interface to bind internal ports. Defaults to `127.0.0.1` (loopback only) for private access via SSH tunnel. For WireGuard VPN access, set this to the host WireGuard IP (e.g. `10.8.0.1`).
* `HOST_IP`: Public host IP for externally exposed listeners (DoT and DoQ on port 853). Run `ip --br a` on the host to identify your public interface address. It is recommended to leave this as `127.0.0.1` during initial setup and update it only after the service is fully verified.
* `AGHUL_VERSION`: Optional image tag pin (defaults to `latest`).

### 4. Initial Bootstrap Setup

Start the temporary bootstrap container to initialize configuration files and run the onboarding wizard:

```bash
cd /home/dns

docker run --rm -it \
  --name adguardhome-bootstrap \
  -u 0:0 \
  -v $(pwd)/data/adguardhome/work:/opt/adguardhome/work \
  -v $(pwd)/data/adguardhome/conf:/opt/adguardhome/conf \
  -v $(pwd)/data/unbound:/opt/unbound \
  -p 127.0.0.1:3000:3000/tcp \
  -e LEGO_ENABLE=false \
  -e LOG_LEVEL=debug \
  ghcr.io/underhax/adguardhome-unbound-lego:latest
```

If configuring on a remote server, establish an SSH tunnel from your local machine:

```bash
ssh -i ~/.ssh/id_ed25519 -p 22 -N -L 3000:127.0.0.1:3000 user@your-server-ip
```

Open `http://localhost:3000` in your local browser, set your administrative username and password, complete the initial wizard, and stop the container using `Ctrl + C` in your terminal.

Restore unprivileged ownership across generated files:

```bash
sudo chown -R dns:dns /home/dns/data
sudo find /home/dns/data -type d -exec chmod 0700 {} +
sudo find /home/dns/data -type f -exec chmod 0600 {} +
```

### 5. Configure Automated TLS with Lego (Optional)

This step is required only when using integrated Lego to obtain and renew Let's Encrypt certificates via Cloudflare DNS-01. If you manage certificates externally, skip to the next step.

Enable Lego in the environment configuration:

```bash
sudo sed -i "s/^LEGO_ENABLE=.*/LEGO_ENABLE=true/" /home/dns/.env
```

Uncomment the service-level and top-level secrets blocks in `docker-compose.yaml`:

```bash
sudo sed -i 's/^    # secrets:/    secrets:/; s/^    #   - /      - /; s/^# secrets:/secrets:/; s/^#   /  /' /home/dns/docker-compose.yaml
```

Create empty secret files with restricted ownership:

```bash
sudo touch /home/dns/secrets/{acme_{domain,email},cf_dns_api_token}
sudo chown root:dns /home/dns/secrets/*
sudo chmod 0440 /home/dns/secrets/*
```

Populate the secrets using an interactive editor:

```bash
sudo nano /home/dns/secrets/acme_domain
sudo nano /home/dns/secrets/acme_email
sudo nano /home/dns/secrets/cf_dns_api_token
```

### 6. Host Network Tuning

Increase UDP receive and transmit buffer limits for DNS-over-QUIC:

```bash
printf '%s\n' \
  'net.core.rmem_max=7500000' \
  'net.core.wmem_max=7500000' | sudo tee /etc/sysctl.d/99-net-buffers.conf
sudo sysctl -p /etc/sysctl.d/99-net-buffers.conf
```

### 7. Start the Service

For initial configuration and testing before configuring a reverse proxy, uncomment the web admin port line in `/home/dns/docker-compose.yaml` (mapped by default to `8002:80`). You can then securely access the administrative panel via a VPN connection or an SSH tunnel.

```bash
docker compose -f /home/dns/docker-compose.yaml up -d
```

### 8. Post-Deployment AdGuard Home Setup

Access the AdGuard Home Web UI (via VPN, local SSH tunnel at `http://localhost:8002`, or later through your Angie reverse proxy):

* In `Settings` -> `DNS settings`, set the Upstream DNS server to `127.0.0.1:5053`.
* In `Settings` -> `General settings`, add `dns.healthcheck.arpa` to `Ignored domains` under both Logs configuration and Statistics configuration.

</details>

---

<details>
<summary><strong>External Angie</strong></summary>

## Edge Reverse Proxy (External Angie)

The `adguardhome` container connects to the shared `frontend_gateway` network (`172.20.0.0/16`) at static IP `172.20.0.253` without publishing HTTP ports on the host. An external Edge Angie instance terminates HTTPS/TLS, manages ACME certificates, and routes encrypted DoH traffic while keeping the administrative interface isolated.

A production-hardened virtual host template for AdGuard Home is maintained in the **[angie-docker-compose](https://github.com/webstudiobond/angie-docker-compose)** repository under `data/conf.d/domains/`:

Download the template [`adguardhome.conf`](https://github.com/webstudiobond/angie-docker-compose/blob/main/data/conf.d/domains/adguardhome.conf) into your Edge Angie domains directory:

```bash
DATA_ANGIE="/home/angie/data"
ANGIE_REPO="https://raw.githubusercontent.com/webstudiobond/angie-docker-compose/main/data/conf.d/domains"

curl -fsSL "${ANGIE_REPO}/adguardhome.conf" -o "${DATA_ANGIE}/conf.d/domains/adguardhome.conf"
```

Open the file to customize your target domain, secret subpath, upstream definitions, and ACME client identifier before reloading Angie:

```bash
sudo nano "${DATA_ANGIE}/conf.d/domains/adguardhome.conf"
```

</details>

---

<details>
<summary><strong>Permissions &amp; Ownership</strong></summary>

### Host Permissions & Privilege Separation

The stack enforces strict host-level privilege separation across three ownership tiers so that even in the event of a container compromise, the process cannot modify deployment manifests or secret files:

* `root:root` (`0600`): `docker-compose.yaml` and `.env` (accessible exclusively to host root).
* `root:dns` (`0750`/`0440`): `/home/dns` base directory (`0750`), `secrets/` directory (`0750`), and secret files (`acme_domain`, `acme_email`, `cf_dns_api_token` at `0440`; read-only to container, immutable to runtime processes).
* `dns:dns` (`0700`/`0600`): `data/` persistent storage (`adguardhome`, `unbound`, `lego`). Managed with strict DAC: directories `0700`, files `0600`.

| Host path | Owner | Mode | Reason |
| --- | --- | --- | --- |
| `/home/dns` | `root:dns` | `0750` | Allow service traversal without access for other users |
| `docker-compose.yaml`, `.env` | `root:root` | `0600` | Host-only manifests and environment settings |
| `secrets/` directory | `root:dns` | `0750` | Traverse-only access for the service group |
| `secrets/*` | `root:dns` | `0440` | Read-only to the service group |
| `data/` directories | `dns:dns` | `0700` | Private writable runtime storage |
| `data/` files | `dns:dns` | `0600` | Private runtime databases, configuration, and certificates |

### One-Shot Permission Reset (Running Stack)

Use this self-contained block to apply or restore canonical ownership and permissions on an existing or migrated installation without stopping the stack:

```bash
sudo chown root:dns /home/dns
sudo chmod 0750 /home/dns

sudo chown root:root /home/dns/{docker-compose.yaml,.env}
sudo chmod 0600 /home/dns/{docker-compose.yaml,.env}

sudo chown -R root:dns /home/dns/secrets
sudo find /home/dns/secrets -type d -exec chmod 0750 {} +
sudo find /home/dns/secrets -type f -exec chmod 0440 {} +

sudo chown -R dns:dns /home/dns/data
sudo find /home/dns/data -type d -exec chmod 0700 {} +
sudo find /home/dns/data -type f -exec chmod 0600 {} +
```

</details>

---

<details>
<summary><strong>Operation &amp; Maintenance</strong></summary>

### Verifying Service Health

Check container status and resource consumption:

```bash
sudo docker compose -f /home/dns/docker-compose.yaml ps
sudo docker stats --no-stream adguardhome
```

### Inspecting Runtime Logs

Follow service logs in real time:

```bash
sudo docker compose -f /home/dns/docker-compose.yaml logs -f
```

### Tearing Down the Stack

Stop and remove the container:

```bash
sudo docker compose -f /home/dns/docker-compose.yaml down
```

</details>

---

<details>
<summary><strong>Development</strong></summary>

For local compilation, testing, static analysis, and CI/CD details, see **[DEVELOPMENT.md](DEVELOPMENT.md)**.

</details>

---

<details>
<summary><strong>License &amp; Attribution</strong></summary>

## License & Attribution

This repository and deployment architecture are licensed under the [MIT License](LICENSE).

**AdGuard Home:** Open-source software developed by [AdguardTeam](https://github.com/AdguardTeam/AdGuardHome) under the [GPL-3.0 License](https://github.com/AdguardTeam/AdGuardHome/blob/master/LICENSE.txt).

**Unbound:** Open-source recursive DNS resolver developed by [NLnet Labs](https://github.com/NLnetLabs/unbound) under the [BSD-3-Clause License](https://github.com/NLnetLabs/unbound/blob/master/LICENSE).

**Lego:** Open-source Let's Encrypt client developed by [go-acme](https://github.com/go-acme/lego) under the [MIT License](https://github.com/go-acme/lego/blob/main/LICENSE).

</details>
