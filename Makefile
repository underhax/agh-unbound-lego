SHELL_SCRIPTS := $(shell find usr/local/bin -type f -name '*.sh')
TEST_SCRIPTS  := $(shell find tests -type f -name '*.sh')
ALL_SCRIPTS   := $(SHELL_SCRIPTS) $(TEST_SCRIPTS)
COVERAGE_DIR  := coverage

.PHONY: check test coverage vulncheck trivy docker-lint \
        shell-lint shell-fmt shell-test shell-coverage shell-verify secrets-scan \
        verify clean

check:
	@test -z "$$(gofmt -s -l .)" || (echo "Unformatted files found. Run 'gofmt -s -w .' to fix them." && false)
	golangci-lint run ./...
	go build ./...

test:
	go test -v -race ./...

coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vulncheck:
	govulncheck ./...

trivy:
	trivy fs --severity CRITICAL,HIGH .

docker-lint:
	hadolint Dockerfile

shell-lint:
	shellcheck --enable=all -x $(ALL_SCRIPTS)
	shfmt -d -s -i 2 $(ALL_SCRIPTS)
	checkbashisms -f $(ALL_SCRIPTS) || true

shell-fmt:
	shfmt -w -s -i 2 $(ALL_SCRIPTS)

shell-test:
	bashunit tests/

shell-coverage:
	rm -rf $(COVERAGE_DIR)
	bashunit tests/ --coverage --coverage-paths usr/local/bin

shell-verify: shell-lint shell-test

secrets-scan:
	gitleaks detect --source . --no-git --no-color

verify: check test vulncheck shell-verify trivy docker-lint secrets-scan

clean:
	rm -rf $(COVERAGE_DIR) .bashunit *.out .gocache
