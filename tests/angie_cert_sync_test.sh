#!/usr/bin/env bash

chown() {
  CHOWN_CALLED=$((CHOWN_CALLED + 1))
  return 0
}

docker() {
  DOCKER_CALLED=$((DOCKER_CALLED + 1))
  return "${DOCKER_EXIT_CODE:-0}"
}

flock() {
  return "${FLOCK_EXIT_CODE:-0}"
}

sleep() {
  SLEEP_CALLED=$((SLEEP_CALLED + 1))
  return 0
}

run_expect_fail() {
  local status=0
  set +e
  (
    set -e
    "${@}"
  ) >/dev/null 2>&1
  status="${?}"
  set -e
  assert_same "1" "${status}"
}

set_up() {
  . ./usr/local/bin/angie_cert_sync.sh
  TEST_TMP_DIR="$(mktemp -d)"
  WORK_DIR="${TEST_TMP_DIR}/work"
  DNS_DIR="${TEST_TMP_DIR}/dns"
  ANGIE_CERTS_DIR="${TEST_TMP_DIR}/angie/certs"
  ENV_FILE="${TEST_TMP_DIR}/angie-cert-sync.env"
  LOCK_FILE="${TEST_TMP_DIR}/lock/angie_cert_sync.lock"
  ANGIE_USER="angie"
  ANGIE_GROUP="angie"
  ANGIE_CONTAINER="angie"
  SETTLE_DELAY="0"
  DOCKER_CALLED=0
  DOCKER_EXIT_CODE=0
  FLOCK_EXIT_CODE=0
  CHOWN_CALLED=0
  SLEEP_CALLED=0

  mkdir -p "${WORK_DIR}" "${DNS_DIR}/secrets" "${DNS_DIR}/data/lego/certificates" "${ANGIE_CERTS_DIR}"
}

tear_down() {
  case "${TEST_TMP_DIR:-}" in
  "") ;;
  *)
    chmod -R u+w "${TEST_TMP_DIR}" 2>/dev/null || true
    rm -rf "${TEST_TMP_DIR:?}"
    ;;
  esac
}

test_usage_outputs_all_options() {
  local output
  output="$(usage)"
  assert_contains "Usage:" "${output}"
  assert_contains "--dns-dir" "${output}"
  assert_contains "--angie-certs" "${output}"
  assert_contains "--angie-user" "${output}"
  assert_contains "--angie-group" "${output}"
  assert_contains "--container" "${output}"
  assert_contains "--settle-delay" "${output}"
  assert_contains "--lock-file" "${output}"
  assert_contains "--env-file" "${output}"
  assert_contains "--help" "${output}"
}

test_die_outputs_error_and_exits_nonzero() {
  local err_file="${TEST_TMP_DIR}/die.err"
  set +e
  (
    set -e
    die "fatal test failure"
  ) 2>"${err_file}"
  local status="${?}"
  set -e

  local output
  output="$(tr -d '\n' <"${err_file}")"
  assert_same "1" "${status}"
  assert_contains "ERROR: fatal test failure" "${output}"
}

test_load_env_file_missing_file_returns_zero() {
  load_env_file "${TEST_TMP_DIR}/nonexistent.env"
  assert_successful_code
}

test_load_env_file_parses_all_supported_keys_and_ignores_comments() {
  cat <<'EOF' >"${ENV_FILE}"
# Comment line

INVALID_LINE_WITHOUT_EQUALS
UNKNOWN_KEY="ignored"
LOCK_FILE="/custom/run/sync.lock"
DNS_DIR='/custom/dns'
ANGIE_CERTS_DIR=/custom/angie/certs
ANGIE_USER="custom_user"
ANGIE_GROUP='custom_group'
ANGIE_CONTAINER=custom_angie
SETTLE_DELAY="7"
EOF

  LOCK_FILE=""
  DNS_DIR=""
  ANGIE_CERTS_DIR=""
  ANGIE_USER=""
  ANGIE_GROUP=""
  ANGIE_CONTAINER=""
  SETTLE_DELAY=""

  load_env_file "${ENV_FILE}"

  assert_same "/custom/run/sync.lock" "${LOCK_FILE}"
  assert_same "/custom/dns" "${DNS_DIR}"
  assert_same "/custom/angie/certs" "${ANGIE_CERTS_DIR}"
  assert_same "custom_user" "${ANGIE_USER}"
  assert_same "custom_group" "${ANGIE_GROUP}"
  assert_same "custom_angie" "${ANGIE_CONTAINER}"
  assert_same "7" "${SETTLE_DELAY}"
}

test_load_env_file_does_not_override_existing_values() {
  printf 'DNS_DIR="/from/env/file"\n' >"${ENV_FILE}"
  DNS_DIR="/from/cli"
  load_env_file "${ENV_FILE}"
  assert_same "/from/cli" "${DNS_DIR}"
}

test_apply_defaults_populates_empty_variables() {
  LOCK_FILE=""
  DNS_DIR=""
  ANGIE_CERTS_DIR=""
  ANGIE_USER=""
  ANGIE_GROUP=""
  ANGIE_CONTAINER=""
  SETTLE_DELAY=""

  apply_defaults

  assert_same "${DEFAULT_LOCK_FILE}" "${LOCK_FILE}"
  assert_same "${DEFAULT_DNS_DIR}" "${DNS_DIR}"
  assert_same "${DEFAULT_ANGIE_CERTS_DIR}" "${ANGIE_CERTS_DIR}"
  assert_same "${DEFAULT_ANGIE_USER}" "${ANGIE_USER}"
  assert_same "${DEFAULT_ANGIE_GROUP}" "${ANGIE_GROUP}"
  assert_same "${DEFAULT_ANGIE_CONTAINER}" "${ANGIE_CONTAINER}"
  assert_same "${DEFAULT_SETTLE_DELAY}" "${SETTLE_DELAY}"
}

test_cleanup_removes_temp_files_and_work_dir() {
  local sub_work="${TEST_TMP_DIR}/cleanup_work"
  local tmp_c="${TEST_TMP_DIR}/tmp.crt"
  local tmp_k="${TEST_TMP_DIR}/tmp.key"
  mkdir -p "${sub_work}"
  printf 'crt\n' >"${tmp_c}"
  printf 'key\n' >"${tmp_k}"

  (
    WORK_DIR="${sub_work}"
    TMP_CRT="${tmp_c}"
    TMP_KEY="${tmp_k}"
    cleanup
  )

  assert_file_not_exists "${tmp_c}"
  assert_file_not_exists "${tmp_k}"
  assert_directory_not_exists "${sub_work}"

  (
    WORK_DIR=""
    TMP_CRT=""
    TMP_KEY=""
    cleanup
  )
  assert_successful_code
}

test_check_dependencies_succeeds_and_fails_when_missing() {
  check_dependencies
  assert_successful_code

  run_expect_fail require_command "missing_dependency_binary_xyz"
}

test_validate_identifier_accepts_valid_and_rejects_invalid() {
  validate_identifier "angie_1-prod.local" "test label"
  assert_successful_code

  local invalid_val
  for invalid_val in "" ".leadingdot" "two..dots" "invalid/slash" "bad space" "semi;colon"; do
    run_expect_fail validate_identifier "${invalid_val}" "test label"
  done
}

test_validate_domain_accepts_valid_domains() {
  validate_domain "example.com"
  validate_domain "dns.example.org"
  validate_domain "sub-1.dns.example.net"
  assert_successful_code
}

test_validate_domain_rejects_invalid_domains() {
  local long_label="aaaaaaaaaabbbbbbbbbbccccccccccddddddddddeeeeeeeeeeffffffffff123"
  local over_253="${long_label}.${long_label}.${long_label}.${long_label}.example.com"

  local invalid_domain
  for invalid_domain in \
    "" \
    "singlelabel" \
    ".example.com" \
    "example.com." \
    "-example.com" \
    "example.com-" \
    "sub..example.com" \
    "sub.-example.com" \
    "sub-.example.com" \
    "sub_underscore.example.com" \
    "example.com/path" \
    "${over_253}"; do
    run_expect_fail validate_domain "${invalid_domain}"
  done
}

test_read_acme_domain_validates_and_trims_secret() {
  printf '  dns.example.com \r\n' >"${DNS_DIR}/secrets/acme_domain"
  local domain
  domain="$(read_acme_domain)"
  assert_same "dns.example.com" "${domain}"
}

test_read_acme_domain_fails_on_missing_or_empty_or_whitespace_secret() {
  run_expect_fail read_acme_domain

  : >"${DNS_DIR}/secrets/acme_domain"
  run_expect_fail read_acme_domain

  printf '   \n\t  ' >"${DNS_DIR}/secrets/acme_domain"
  run_expect_fail read_acme_domain
}

test_reload_angie_container_tolerates_stopped_container() {
  DOCKER_EXIT_CODE=1
  local out_file="${TEST_TMP_DIR}/reload.out"
  reload_angie_container >"${out_file}"
  local output
  output="$(tr -d '\n' <"${out_file}")"
  assert_contains "Sent SIGHUP reload signal to container angie." "${output}"
  assert_same "1" "${DOCKER_CALLED}"
}

test_sync_certificates_skips_when_source_files_missing_or_empty() {
  local output
  output="$(sync_certificates "dns.example.com")"
  assert_contains "not ready, skipping synchronization" "${output}"
  assert_same "0" "${DOCKER_CALLED}"

  printf 'cert-only\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.crt"
  output="$(sync_certificates "dns.example.com")"
  assert_contains "not ready, skipping synchronization" "${output}"
  assert_same "0" "${DOCKER_CALLED}"

  : >"${DNS_DIR}/data/lego/certificates/dns.example.com.key"
  output="$(sync_certificates "dns.example.com")"
  assert_contains "not ready, skipping synchronization" "${output}"
  assert_same "0" "${DOCKER_CALLED}"
}

test_sync_certificates_creates_underscored_dir_and_reloads() {
  printf 'cert-v1\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.crt"
  printf 'key-v1\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.key"

  sync_certificates "dns.example.com" >/dev/null

  assert_file_exists "${ANGIE_CERTS_DIR}/dns_example_com/dns.example.com.crt"
  assert_file_exists "${ANGIE_CERTS_DIR}/dns_example_com/dns.example.com.key"
  assert_same "1" "${DOCKER_CALLED}"
  assert_same "2" "${CHOWN_CALLED}"
}

test_sync_certificates_skips_when_already_up_to_date() {
  printf 'cert-v1\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.crt"
  printf 'key-v1\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.key"

  sync_certificates "dns.example.com" >/dev/null
  DOCKER_CALLED=0

  local output
  output="$(sync_certificates "dns.example.com")"
  assert_contains "already up to date" "${output}"
  assert_same "0" "${DOCKER_CALLED}"
}

test_sync_certificates_updates_when_cert_or_key_renewed() {
  printf 'cert-v1\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.crt"
  printf 'key-v1\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.key"
  sync_certificates "dns.example.com" >/dev/null

  printf 'cert-v1\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.crt"
  printf 'key-v2\n' >"${DNS_DIR}/data/lego/certificates/dns.example.com.key"
  DOCKER_CALLED=0

  sync_certificates "dns.example.com" >/dev/null

  local key_content
  key_content="$(tr -d '\n' <"${ANGIE_CERTS_DIR}/dns_example_com/dns.example.com.key")"
  assert_same "key-v2" "${key_content}"
  assert_same "1" "${DOCKER_CALLED}"
}

test_main_help_flag_exits_zero() {
  local output
  output="$(main --help)"
  assert_contains "Usage:" "${output}"

  output="$(main -h)"
  assert_contains "Usage:" "${output}"
}

test_main_parses_all_cli_options_and_executes_sync() {
  printf 'dns.example.net\n' >"${DNS_DIR}/secrets/acme_domain"
  printf 'cert-net\n' >"${DNS_DIR}/data/lego/certificates/dns.example.net.crt"
  printf 'key-net\n' >"${DNS_DIR}/data/lego/certificates/dns.example.net.key"
  : >"${ENV_FILE}"

  (
    main \
      -d "${DNS_DIR}" \
      -c "${ANGIE_CERTS_DIR}" \
      -u "angie_u" \
      -g "angie_g" \
      -n "angie_c" \
      -s "3" \
      -l "${LOCK_FILE}" \
      --env-file "${ENV_FILE}" >/dev/null
  )

  assert_file_exists "${ANGIE_CERTS_DIR}/dns_example_net/dns.example.net.crt"
  assert_file_exists "${ANGIE_CERTS_DIR}/dns_example_net/dns.example.net.key"
}

test_main_rejects_missing_option_arguments_and_unknown_flags() {
  local flag
  for flag in -d --dns-dir -c --angie-certs -u --angie-user -g --angie-group -n --container -s --settle-delay -l --lock-file --env-file --unknown-flag; do
    run_expect_fail main "${flag}"
  done
}

test_main_rejects_invalid_settle_delay() {
  run_expect_fail main -s "not_a_number"
}

test_main_fails_when_lock_is_already_held() {
  FLOCK_EXIT_CODE=1
  run_expect_fail main -d "${DNS_DIR}" -c "${ANGIE_CERTS_DIR}" -l "${LOCK_FILE}" -s "0"
}

test_direct_script_invocation_with_trace() {
  local expected_dir="${ANGIE_CERTS_DIR}/dns_example_org"
  printf 'dns.example.org\n' >"${DNS_DIR}/secrets/acme_domain"
  printf 'cert-org\n' >"${DNS_DIR}/data/lego/certificates/dns.example.org.crt"
  printf 'key-org\n' >"${DNS_DIR}/data/lego/certificates/dns.example.org.key"

  (
    TRACE=1
    ANGIE_CERT_SYNC_AUTORUN=1
    set -- \
      --dns-dir "${DNS_DIR}" \
      --angie-certs "${ANGIE_CERTS_DIR}" \
      --angie-user "angie" \
      --angie-group "angie" \
      --container "angie" \
      --lock-file "${LOCK_FILE}" \
      --env-file "${ENV_FILE}" \
      --settle-delay "0"
    . ./usr/local/bin/angie_cert_sync.sh >/dev/null 2>&1
  )

  assert_file_exists "${expected_dir}/dns.example.org.crt"
  assert_file_exists "${expected_dir}/dns.example.org.key"
}
