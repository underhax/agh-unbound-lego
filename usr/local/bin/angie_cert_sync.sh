#!/usr/bin/env bash
set -euo pipefail
umask 077

case "${TRACE:-0}" in
1)
  set -x
  ;;
*)
  :
  ;;
esac

SCRIPT_NAME="${0##*/}"
LOCK_FD=200

DEFAULT_LOCK_FILE="/run/lock/angie_cert_sync.lock"
DEFAULT_ENV_FILE="/etc/default/angie-cert-sync"
DEFAULT_DNS_DIR="/home/dns"
DEFAULT_ANGIE_CERTS_DIR="/home/angie/data/certs"
DEFAULT_ANGIE_USER="angie"
DEFAULT_ANGIE_GROUP="angie"
DEFAULT_ANGIE_CONTAINER="angie"
DEFAULT_SETTLE_DELAY="2"

LOCK_FILE="${LOCK_FILE:-}"
ENV_FILE="${DEFAULT_ENV_FILE}"
DNS_DIR="${DNS_DIR:-}"
ANGIE_CERTS_DIR="${ANGIE_CERTS_DIR:-}"
ANGIE_USER="${ANGIE_USER:-}"
ANGIE_GROUP="${ANGIE_GROUP:-}"
ANGIE_CONTAINER="${ANGIE_CONTAINER:-}"
SETTLE_DELAY="${SETTLE_DELAY:-}"

WORK_DIR=""
TMP_CRT=""
TMP_KEY=""

usage() {
  printf 'Usage: %s [OPTIONS]\n\n' "${SCRIPT_NAME}"
  printf 'Options:\n'
  printf '  -d, --dns-dir DIR          Base directory of the DNS stack (default: %s)\n' "${DEFAULT_DNS_DIR}"
  printf '  -c, --angie-certs DIR      Angie certificates root directory (default: %s)\n' "${DEFAULT_ANGIE_CERTS_DIR}"
  printf '  -u, --angie-user USER      Owner user for synced certificates (default: %s)\n' "${DEFAULT_ANGIE_USER}"
  printf '  -g, --angie-group GROUP    Owner group for synced certificates (default: %s)\n' "${DEFAULT_ANGIE_GROUP}"
  printf '  -n, --container NAME       Angie Docker container name (default: %s)\n' "${DEFAULT_ANGIE_CONTAINER}"
  printf '  -s, --settle-delay SEC     Delay in seconds before reading certificates (default: %s)\n' "${DEFAULT_SETTLE_DELAY}"
  printf '  -l, --lock-file FILE       Path to exclusive lock file (default: %s)\n' "${DEFAULT_LOCK_FILE}"
  printf '      --env-file FILE        Path to environment file (default: %s)\n' "${DEFAULT_ENV_FILE}"
  printf '  -h, --help                 Display this help message and exit\n'
}

die() {
  local message="${1}"
  printf 'ERROR: %s\n' "${message}" >&2
  exit 1
}

load_env_file() {
  local file_path="${1}"
  case "$(test -f "${file_path}" && printf "yes")" in
  "yes")
    :
    ;;
  *)
    return 0
    ;;
  esac

  local line key val
  while IFS= read -r line || test -n "${line}"; do
    case "${line}" in
    "" | \#*)
      continue
      ;;
    *=*)
      key="${line%%=*}"
      val="${line#*=}"
      val="${val#\"}"
      val="${val%\"}"
      val="${val#\'}"
      val="${val%\'}"
      case "${key}" in
      LOCK_FILE)
        LOCK_FILE="${LOCK_FILE:-${val}}"
        ;;
      DNS_DIR)
        DNS_DIR="${DNS_DIR:-${val}}"
        ;;
      ANGIE_CERTS_DIR)
        ANGIE_CERTS_DIR="${ANGIE_CERTS_DIR:-${val}}"
        ;;
      ANGIE_USER)
        ANGIE_USER="${ANGIE_USER:-${val}}"
        ;;
      ANGIE_GROUP)
        ANGIE_GROUP="${ANGIE_GROUP:-${val}}"
        ;;
      ANGIE_CONTAINER)
        ANGIE_CONTAINER="${ANGIE_CONTAINER:-${val}}"
        ;;
      SETTLE_DELAY)
        SETTLE_DELAY="${SETTLE_DELAY:-${val}}"
        ;;
      *)
        :
        ;;
      esac
      ;;
    *)
      :
      ;;
    esac
  done <"${file_path}"
}

apply_defaults() {
  LOCK_FILE="${LOCK_FILE:-${DEFAULT_LOCK_FILE}}"
  DNS_DIR="${DNS_DIR:-${DEFAULT_DNS_DIR}}"
  ANGIE_CERTS_DIR="${ANGIE_CERTS_DIR:-${DEFAULT_ANGIE_CERTS_DIR}}"
  ANGIE_USER="${ANGIE_USER:-${DEFAULT_ANGIE_USER}}"
  ANGIE_GROUP="${ANGIE_GROUP:-${DEFAULT_ANGIE_GROUP}}"
  ANGIE_CONTAINER="${ANGIE_CONTAINER:-${DEFAULT_ANGIE_CONTAINER}}"
  SETTLE_DELAY="${SETTLE_DELAY:-${DEFAULT_SETTLE_DELAY}}"
}

cleanup() {
  local exit_code="${?}"
  trap - EXIT SIGINT SIGTERM
  case "${TMP_CRT}" in
  "")
    :
    ;;
  *)
    rm -f "${TMP_CRT}"
    ;;
  esac
  case "${TMP_KEY}" in
  "")
    :
    ;;
  *)
    rm -f "${TMP_KEY}"
    ;;
  esac
  case "${WORK_DIR}" in
  "")
    :
    ;;
  *)
    rm -rf "${WORK_DIR:?}"
    ;;
  esac
  flock -u "${LOCK_FD}" 2>/dev/null || true
  exit "${exit_code}"
}

require_command() {
  local dep="${1}"
  command -v "${dep}" >/dev/null 2>&1 || die "Required dependency '${dep}' is not installed or not in PATH"
}

check_dependencies() {
  local dep
  for dep in docker flock mktemp cmp cp mv chmod chown mkdir rm tr sleep; do
    require_command "${dep}"
  done
}

validate_identifier() {
  local value="${1}"
  local label="${2}"
  case "${value}" in
  "" | *[!a-zA-Z0-9_.-]* | .* | *..*)
    die "Invalid ${label}: '${value}'"
    ;;
  *)
    :
    ;;
  esac
}

validate_domain() {
  local domain="${1}"
  case "${#domain}" in
  0)
    die "ACME domain cannot be empty"
    ;;
  *)
    :
    ;;
  esac

  case "$((${#domain} > 253))" in
  1)
    die "ACME domain exceeds maximum length of 253 characters: '${domain}'"
    ;;
  *)
    :
    ;;
  esac

  case "${domain}" in
  *[!a-zA-Z0-9.-]* | .* | *. | -* | *- | *..* | *.-* | *-.*)
    die "Invalid ACME domain format: '${domain}'"
    ;;
  *.*)
    :
    ;;
  *)
    die "ACME domain must contain at least two labels: '${domain}'"
    ;;
  esac
}

read_acme_domain() {
  local secret_file="${DNS_DIR}/secrets/acme_domain"
  case "$(test -s "${secret_file}" && printf "yes")" in
  "yes")
    :
    ;;
  *)
    die "ACME domain secret file not found or empty: '${secret_file}'"
    ;;
  esac

  local domain
  domain="$(tr -d '[:space:]' <"${secret_file}")"
  validate_domain "${domain}"
  printf '%s\n' "${domain}"
}

reload_angie_container() {
  docker kill --signal=HUP "${ANGIE_CONTAINER}" >/dev/null 2>&1 || true
  printf 'Sent SIGHUP reload signal to container %s.\n' "${ANGIE_CONTAINER}"
}

sync_certificates() {
  local domain="${1}"
  local domain_dir
  domain_dir="$(printf '%s' "${domain}" | tr '.' '_')"

  local src_dir="${DNS_DIR}/data/lego/certificates"
  local src_crt="${src_dir}/${domain}.crt"
  local src_key="${src_dir}/${domain}.key"

  case "$(test -s "${src_crt}" && test -s "${src_key}" && printf "yes")" in
  "yes")
    :
    ;;
  *)
    printf 'Source certificate or key for %s is not ready, skipping synchronization.\n' "${domain}"
    return 0
    ;;
  esac

  local target_dir="${ANGIE_CERTS_DIR}/${domain_dir}"
  local dst_crt="${target_dir}/${domain}.crt"
  local dst_key="${target_dir}/${domain}.key"

  case "$(cmp -s "${src_crt}" "${dst_crt}" 2>/dev/null && cmp -s "${src_key}" "${dst_key}" 2>/dev/null && printf "yes")" in
  "yes")
    printf 'Certificates for %s are already up to date.\n' "${domain}"
    return 0
    ;;
  *)
    :
    ;;
  esac

  mkdir -p "${target_dir}"
  chmod 0700 "${target_dir}"

  TMP_CRT="$(mktemp "${target_dir}/.${domain}.crt.XXXXXX")"
  TMP_KEY="$(mktemp "${target_dir}/.${domain}.key.XXXXXX")"

  cp "${src_crt}" "${TMP_CRT}"
  cp "${src_key}" "${TMP_KEY}"

  chmod 0400 "${TMP_CRT}" "${TMP_KEY}"
  chown "${ANGIE_USER}:${ANGIE_GROUP}" "${TMP_CRT}" "${TMP_KEY}"

  mv -f "${TMP_CRT}" "${dst_crt}"
  TMP_CRT=""
  mv -f "${TMP_KEY}" "${dst_key}"
  TMP_KEY=""

  chown "${ANGIE_USER}:${ANGIE_GROUP}" "${target_dir}"
  chmod 0500 "${target_dir}"

  printf 'Synchronized TLS certificates for %s to %s.\n' "${domain}" "${target_dir}"
  reload_angie_container
}

main() {
  while true; do
    case "${#}" in
    0)
      break
      ;;
    *)
      :
      ;;
    esac

    case "${1}" in
    -h | --help)
      usage
      exit 0
      ;;
    -d | --dns-dir)
      case "${#}" in
      0 | 1)
        die "Option '${1}' requires an argument"
        ;;
      *)
        :
        ;;
      esac
      DNS_DIR="${2}"
      shift 2
      ;;
    -c | --angie-certs)
      case "${#}" in
      0 | 1)
        die "Option '${1}' requires an argument"
        ;;
      *)
        :
        ;;
      esac
      ANGIE_CERTS_DIR="${2}"
      shift 2
      ;;
    -u | --angie-user)
      case "${#}" in
      0 | 1)
        die "Option '${1}' requires an argument"
        ;;
      *)
        :
        ;;
      esac
      ANGIE_USER="${2}"
      shift 2
      ;;
    -g | --angie-group)
      case "${#}" in
      0 | 1)
        die "Option '${1}' requires an argument"
        ;;
      *)
        :
        ;;
      esac
      ANGIE_GROUP="${2}"
      shift 2
      ;;
    -n | --container)
      case "${#}" in
      0 | 1)
        die "Option '${1}' requires an argument"
        ;;
      *)
        :
        ;;
      esac
      ANGIE_CONTAINER="${2}"
      shift 2
      ;;
    -s | --settle-delay)
      case "${#}" in
      0 | 1)
        die "Option '${1}' requires an argument"
        ;;
      *)
        :
        ;;
      esac
      SETTLE_DELAY="${2}"
      shift 2
      ;;
    -l | --lock-file)
      case "${#}" in
      0 | 1)
        die "Option '${1}' requires an argument"
        ;;
      *)
        :
        ;;
      esac
      LOCK_FILE="${2}"
      shift 2
      ;;
    --env-file)
      case "${#}" in
      0 | 1)
        die "Option '${1}' requires an argument"
        ;;
      *)
        :
        ;;
      esac
      ENV_FILE="${2}"
      shift 2
      ;;
    *)
      die "Unknown option: '${1}'"
      ;;
    esac
  done

  load_env_file "${ENV_FILE}"
  apply_defaults

  case "${SETTLE_DELAY}" in
  "" | *[!0-9]*)
    die "Settle delay must be a non-negative integer: '${SETTLE_DELAY}'"
    ;;
  *)
    :
    ;;
  esac

  validate_identifier "${ANGIE_USER}" "Angie owner user"
  validate_identifier "${ANGIE_GROUP}" "Angie owner group"
  validate_identifier "${ANGIE_CONTAINER}" "Angie container name"

  check_dependencies

  WORK_DIR="$(mktemp -d)"
  trap cleanup EXIT SIGINT SIGTERM

  mkdir -p "${LOCK_FILE%/*}"
  exec 200>"${LOCK_FILE}"
  flock -n 200 || die "Another certificate synchronization holds the lock at '${LOCK_FILE}'"

  case "${SETTLE_DELAY}" in
  0)
    :
    ;;
  *)
    sleep "${SETTLE_DELAY}"
    ;;
  esac

  local domain
  domain="$(read_acme_domain)"
  sync_certificates "${domain}"
}

case "${ANGIE_CERT_SYNC_AUTORUN:-${0##*/}}" in
"angie_cert_sync.sh" | "1")
  main "${@}"
  ;;
*)
  :
  ;;
esac
