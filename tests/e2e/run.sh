#!/usr/bin/env bash
# Dispatcher: runs one named e2e case. Each case owns a subdirectory here
# with its own run.sh, fixtures, and README. Use --list to enumerate cases.
#
# Usage:
#   tests/e2e/run.sh [--case <name>] [--list] [-- extra args...]
#
# When --case is omitted the "dsv" case runs (back-compat with the pre-split
# layout). Extra args after the case name are forwarded to the per-case
# run.sh. Per-case scripts are also runnable directly
# (e.g. tests/e2e/basic-binding/run.sh) for developers iterating on a single
# case without the dispatcher.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

list_cases() {
  # A case is any immediate subdir with an executable run.sh.
  for d in "${script_dir}"/*/; do
    [[ -d "${d}" ]] || continue
    name=$(basename "${d}")
    [[ -x "${d}run.sh" ]] || continue
    printf '%s\n' "${name}"
  done
}

case_name="dsv"
args=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --case)
      [[ $# -ge 2 ]] || { printf 'tests/e2e: --case requires a value\n' >&2; exit 2; }
      case_name="$2"
      shift 2
      ;;
    --case=*)
      case_name="${1#--case=}"
      shift
      ;;
    --list)
      list_cases
      exit 0
      ;;
    --)
      shift
      args+=("$@")
      break
      ;;
    -h|--help)
      cat <<'EOF' >&2
Usage: tests/e2e/run.sh [--case <name>] [--list] [-- extra args...]

Options:
  --case <name>   Select a case subdirectory (default: dsv)
  --list          List available cases and exit
  --help          Show this message

Cases are discovered as immediate subdirectories containing an executable
run.sh. Arguments after `--` are forwarded to the per-case run.sh.
EOF
      exit 0
      ;;
    *)
      args+=("$1")
      shift
      ;;
  esac
done

case_dir="${script_dir}/${case_name}"
case_script="${case_dir}/run.sh"
if [[ ! -d "${case_dir}" ]]; then
  printf 'tests/e2e: case %q not found at %s\n' "${case_name}" "${case_dir}" >&2
  printf 'available cases:\n' >&2
  list_cases | sed 's/^/  /' >&2
  exit 1
fi
if [[ ! -x "${case_script}" ]]; then
  printf 'tests/e2e: %s is not executable\n' "${case_script}" >&2
  exit 1
fi

exec "${case_script}" "${args[@]}"
