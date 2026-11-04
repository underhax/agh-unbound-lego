# Development Guide

## Prerequisites

Ensure the following dependencies are installed prior to local development:

* Git
* Go 1.27+
* GNU Make
* golangci-lint (Go code static analysis)
* govulncheck (Go vulnerability scanner)
* hadolint (Dockerfile linter)
* trivy (filesystem and vulnerability scanner)

## Repository Initialization

1. **Clone the repository:**
   ```bash
   git clone https://github.com/underhax/adguardhome-unbound-lego.git
   cd adguardhome-unbound-lego
   ```

2. **Install Security and Quality Tooling:**
   Install required linters and scanners utilized in CI pipelines and local verification:
   * **golangci-lint:** [https://golangci-lint.run/docs/welcome/install/](https://golangci-lint.run/docs/welcome/install/)
   * **govulncheck:** `go install golang.org/x/vuln/cmd/govulncheck@latest`
   * **hadolint:** [https://github.com/hadolint/hadolint#install](https://github.com/hadolint/hadolint#install)
   * **trivy:** [https://trivy.dev/docs/latest/getting-started/](https://trivy.dev/docs/latest/getting-started/)

3. **Development Files Layout:**
   ```text
   ├── Dockerfile                     # Multi-stage scratch container build
   ├── Makefile                       # Validation, test, and linting targets
   ├── .golangci.yml                  # Strict Go linter configuration
   ├── go.mod                         # Go module definition
   ├── cmd/
   │   └── supervisor/                # Custom Go process supervisor entrypoint
   ├── internal/
   │   ├── acme/                      # Lego ACME DNS-01 runner and cert watcher
   │   ├── config/                    # Configuration loading and defaults
   │   ├── health/                    # Background healthcheck engine
   │   ├── process/                   # Subprocess lifecycle manager (AGH, Unbound)
   │   ├── setup/                     # Initialization and runtime directory bootstrapping
   │   └── util/                      # Common helpers and signal handlers
   ├── build/
   │   └── unbound.conf.default       # Hardened Unbound recursive configuration
   ├── docker-compose.dev.yaml        # Local development build and debug manifest
   └── .env                           # Local environment configuration
   ```

## Code Validation & Testing

All validation steps are centralized in the [`Makefile`](Makefile).

1. **Code Formatting, Linting & Unit Tests:**
   Validate Go formatting (`gofmt -s`), execute `golangci-lint`, verify compilation, and run race-enabled unit tests:
   ```bash
   make check
   make test
   ```

2. **Vulnerability & Container Scanning:**
   Execute Go vulnerability analysis and filesystem security scanning:
   ```bash
   make vulncheck
   make trivy
   make docker-lint
   ```

3. **Code Coverage:**
   Execute unit tests with the race detector enabled and generate a statement coverage report:
   ```bash
   make coverage
   ```
   To remove generated coverage artifacts:
   ```bash
   make clean
   ```

4. **Complete Project Verification:**
   Prior to committing changes or submitting a pull request, execute the complete validation suite:
   ```bash
   make verify
   ```

## Build & Local Development Workflow

1. **Compile the Go supervisor locally:**
   ```bash
   CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /dev/null ./cmd/supervisor
   ```

2. **Build the container image using Docker Compose:**
   ```bash
   docker compose -f docker-compose.dev.yaml build --progress=plain
   ```

   To rebuild without utilizing cached layers:
   ```bash
   docker compose -f docker-compose.dev.yaml build --progress=plain --no-cache
   ```

   To pull the latest upstream builder images:
   ```bash
   docker compose -f docker-compose.dev.yaml build --progress=plain --pull --no-cache
   ```

3. **Initial bootstrap with the local image:**
   When bootstrapping a fresh environment, run the setup wizard using the local development image:
   ```bash
   docker run --rm -it \
     --name adguardhome-dev-bootstrap \
     -u 0:0 \
     -v $(pwd)/data/adguardhome/work:/opt/adguardhome/work \
     -v $(pwd)/data/adguardhome/conf:/opt/adguardhome/conf \
     -v $(pwd)/data/unbound:/opt/unbound \
     -p 127.0.0.1:3000:3000/tcp \
     -e LEGO_ENABLE=false \
     -e LOG_LEVEL=debug \
     adguardhome-unbound-lego-dev:latest
   ```
   Follow the deployment procedure in [README.md](README.md) to complete initial onboarding, restore file ownership, and manage the stack via `docker-compose.dev.yaml`.

## Makefile Targets Reference

| Target | Description |
| --- | --- |
| `make check` | Validates formatting (`gofmt -s`), executes `golangci-lint`, and verifies compilation. |
| `make test` | Runs the Go unit test suite with data race detection enabled (`-race`). |
| `make coverage` | Generates a function-level statement coverage report (`coverage.out`). |
| `make vulncheck` | Analyzes Go source code and dependencies for known vulnerabilities via `govulncheck`. |
| `make docker-lint` | Lints the multi-stage `Dockerfile` using `hadolint`. |
| `make trivy` | Scans the filesystem for `CRITICAL` and `HIGH` severity vulnerabilities using `trivy`. |
| `make verify` | Sequentially executes `check`, `test`, `vulncheck`, `trivy`, and `docker-lint`. |
| `make clean` | Removes generated test artifacts and coverage profiles. |
