#!/usr/bin/env bash
# nginx-metallb-binding e2e case: exercises the lb-ip-pool capability.
# nginx-ingress (consumer) binds metallb (provider) via a single binding
# named `nginx-ingress-lb`. Expand must synthesize:
#   - provider: basic-infra-dsv/packages/metallb/resources/pool/<binding-suffix>/
#   - consumer: basic-infra-dsv/packages/nginx-ingress/resources/lb-binding/<binding-suffix>/
# and render a MetalLB IPAddressPool + L2Advertisement on the provider side
# plus a documentation ConfigMap on the consumer side.
#
# No placeholders are introduced by this binding (all values are literal), so
# the case runs check -> expand -> generate without a fill pass.
#
# Env vars:
#   GITUPS_OUTPUT_DIR  (default: gitups-output-dir)
#   KUBE_CONTEXT       (default: kind-nginx-metallb-binding)
#   GITUPS_E2E_APPLY   (default: false)

set -euo pipefail

case_name="nginx-metallb-binding"
env_key="dsv"
pool_name="ingress-pool"
pool_cidr="172.18.255.200-172.18.255.210"
binding_name="nginx-ingress-lb"
suffix="basic-infra-${env_key}"
kubectl_context="${KUBE_CONTEXT:-kind-${case_name}}"
output_dir_arg="${GITUPS_OUTPUT_DIR:-gitups-output-dir}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../../.." && pwd)"
bin="${repo_root}/bin/gitups"

case "${output_dir_arg}" in
  /*) output_dir="${output_dir_arg}" ;;
  *) output_dir="${repo_root}/${output_dir_arg}" ;;
esac

workspace="${output_dir}/${case_name}"
provision_src="${script_dir}/provision.yaml"

if [[ ! -x "${bin}" ]]; then
  printf 'tests/e2e/%s: %s is missing; run make build first\n' "${case_name}" "${bin}" >&2
  exit 1
fi
if [[ ! -f "${provision_src}" ]]; then
  printf 'tests/e2e/%s: provision fixture not found: %s\n' "${case_name}" "${provision_src}" >&2
  exit 1
fi

for tool in kubectl helm kustomize python3; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    printf 'tests/e2e/%s: required tool not found in PATH: %s\n' "${case_name}" "${tool}" >&2
    exit 1
  fi
done

if [[ "${GITUPS_E2E_APPLY:-false}" == "true" ]]; then
  kubectl --context "${kubectl_context}" cluster-info >/dev/null
fi

rm -rf "${workspace}"
mkdir -p "${workspace}"
cp "${provision_src}" "${workspace}/provision.yaml"

"${bin}" check "${case_name}" --output-dir "${output_dir}"
"${bin}" expand "${case_name}" --output-dir "${output_dir}" --force

placeholder_count=$(python3 - "${workspace}/full-provision.yaml" <<'PY'
import sys, yaml
with open(sys.argv[1]) as f:
    doc = yaml.safe_load(f)
phs = (doc.get("spec", {}) or {}).get("placeholders") or []
print(len(phs))
PY
)
if [[ "${placeholder_count}" != "0" ]]; then
  printf 'tests/e2e/%s: expected 0 placeholders, got %s\n' "${case_name}" "${placeholder_count}" >&2
  python3 - "${workspace}/full-provision.yaml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
for ph in (doc.get("spec", {}) or {}).get("placeholders") or []:
    print(f"  {ph.get('path')} \u2014 {ph.get('reason')}")
PY
  exit 1
fi

"${bin}" check "${case_name}" --output-dir "${output_dir}"
"${bin}" generate "${case_name}" --output-dir "${output_dir}" --context "${case_name}" --prune

expected_repos=(
  "basic-infra"
  "basic-infra-${env_key}"
)
for repo in "${expected_repos[@]}"; do
  if [[ ! -d "${workspace}/${repo}" ]]; then
    printf 'tests/e2e/%s: expected repo dir missing: %s\n' "${case_name}" "${repo}" >&2
    exit 1
  fi
done

provider_dir="${workspace}/basic-infra-${env_key}/packages/metallb/resources/pool/${binding_name}-${suffix}"
consumer_dir="${workspace}/basic-infra-${env_key}/packages/nginx-ingress/resources/lb-binding/${binding_name}-${suffix}"

for f in ipaddresspool.yaml l2advertisement.yaml kustomization.yaml; do
  if [[ ! -f "${provider_dir}/${f}" ]]; then
    printf 'tests/e2e/%s: missing provider-side artifact: %s/%s\n' "${case_name}" "${provider_dir}" "${f}" >&2
    exit 1
  fi
done

if ! grep -q "name: ${pool_name}" "${provider_dir}/ipaddresspool.yaml"; then
  printf 'tests/e2e/%s: provider IPAddressPool missing pool name %q\n' "${case_name}" "${pool_name}" >&2
  exit 1
fi
if ! grep -q "${pool_cidr}" "${provider_dir}/ipaddresspool.yaml"; then
  printf 'tests/e2e/%s: provider IPAddressPool missing CIDR %q\n' "${case_name}" "${pool_cidr}" >&2
  exit 1
fi
if ! grep -qE "^[[:space:]]*-[[:space:]]+${pool_name}\$" "${provider_dir}/l2advertisement.yaml"; then
  printf 'tests/e2e/%s: provider L2Advertisement not scoped to pool %q\n' "${case_name}" "${pool_name}" >&2
  exit 1
fi

for f in configmap.yaml kustomization.yaml; do
  if [[ ! -f "${consumer_dir}/${f}" ]]; then
    printf 'tests/e2e/%s: missing consumer-side artifact: %s/%s\n' "${case_name}" "${consumer_dir}" "${f}" >&2
    exit 1
  fi
done

if ! grep -q "poolName: ${pool_name}" "${consumer_dir}/configmap.yaml"; then
  printf 'tests/e2e/%s: consumer ConfigMap missing poolName %q\n' "${case_name}" "${pool_name}" >&2
  exit 1
fi

if ! grep -q 'gitups.io/apply-wave' "${provider_dir}/kustomization.yaml"; then
  printf 'tests/e2e/%s: provider kustomization missing apply-wave annotation\n' "${case_name}" >&2
  exit 1
fi
if ! grep -q 'gitups.io/apply-wave' "${consumer_dir}/kustomization.yaml"; then
  printf 'tests/e2e/%s: consumer kustomization missing apply-wave annotation\n' "${case_name}" >&2
  exit 1
fi

provider_wave=$(python3 - "${provider_dir}/kustomization.yaml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
print(doc["commonAnnotations"]["gitups.io/apply-wave"])
PY
)
consumer_wave=$(python3 - "${consumer_dir}/kustomization.yaml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
print(doc["commonAnnotations"]["gitups.io/apply-wave"])
PY
)
if [[ "${provider_wave}" -ge "${consumer_wave}" ]]; then
  printf 'tests/e2e/%s: provider wave (%s) must be < consumer wave (%s)\n' \
    "${case_name}" "${provider_wave}" "${consumer_wave}" >&2
  exit 1
fi

if grep -r --exclude=full-provision.yaml -l '__GITUPS_PLACEHOLDER__' "${workspace}" >/dev/null 2>&1; then
  printf 'tests/e2e/%s: placeholder sentinel leaked into rendered tree:\n' "${case_name}" >&2
  grep -r --exclude=full-provision.yaml -l '__GITUPS_PLACEHOLDER__' "${workspace}" >&2 || true
  exit 1
fi

"${bin}" status "${case_name}" --output-dir "${output_dir}"

if [[ "${GITUPS_E2E_APPLY:-false}" == "true" ]]; then
  "${bin}" apply "${case_name}" --output-dir "${output_dir}" --to "${kubectl_context}" --wait-crds
  "${bin}" wait "${case_name}" --output-dir "${output_dir}" --to "${kubectl_context}" --timeout "${GITUPS_E2E_WAIT_TIMEOUT:-10m}"
fi

printf 'tests/e2e/%s: completed env=%s output=%s context=%s apply=%s\n' \
  "${case_name}" "${env_key}" "${workspace}" "${kubectl_context}" "${GITUPS_E2E_APPLY:-false}" >&2
