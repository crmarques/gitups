#!/usr/bin/env bash
# dsv e2e case: exercises the full init->expand->fill->check->generate->
# (apply+wait) flow against a comprehensive Provision covering every
# renderer. No capability bindings are used.
#
# Env vars:
#   METALLB_ADDRESS_POOL    (default: 172.18.255.200-172.18.255.250)
#   GITOPS_REPO_URL         (default: https://example.invalid/gitops/gitops-controllers-dsv.git)
#   VAULT_DEV_ROOT_TOKEN    (default: gitups-e2e-root)
#   KUBE_CONTEXT            (default: kind-dsv)
#   GITUPS_E2E_APPLY        (default: true — set to "false" to skip cluster steps)
#   GITUPS_E2E_WAIT_TIMEOUT (default: 10m)

set -euo pipefail

env_name="dsv"
kubectl_context="${KUBE_CONTEXT:-kind-${env_name}}"
output_dir_arg="${GITUPS_OUTPUT_DIR:-gitups-output-dir}"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../../.." && pwd)"
bin="${repo_root}/bin/gitups"

case "${output_dir_arg}" in
  /*) output_dir="${output_dir_arg}" ;;
  *) output_dir="${repo_root}/${output_dir_arg}" ;;
esac

workspace="${output_dir}/${env_name}"
provision_src="${script_dir}/provision.yaml"
metallb_pool="${METALLB_ADDRESS_POOL:-172.18.255.200-172.18.255.250}"
gitops_repo_url="${GITOPS_REPO_URL:-https://example.invalid/gitops/gitops-controllers-${env_name}.git}"
vault_root_token="${VAULT_DEV_ROOT_TOKEN:-gitups-e2e-root}"
wait_timeout="${GITUPS_E2E_WAIT_TIMEOUT:-10m}"

if [[ ! -x "${bin}" ]]; then
  printf 'tests/e2e/dsv: %s is missing; run make build first\n' "${bin}" >&2
  exit 1
fi
if [[ ! -f "${provision_src}" ]]; then
  printf 'tests/e2e/dsv: provision fixture not found: %s\n' "${provision_src}" >&2
  exit 1
fi

for tool in kubectl helm kustomize go; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    printf 'tests/e2e/dsv: required tool not found in PATH: %s\n' "${tool}" >&2
    exit 1
  fi
done

if [[ "${GITUPS_E2E_APPLY:-true}" == "true" ]]; then
  kubectl --context "${kubectl_context}" cluster-info >/dev/null
fi

rm -rf "${workspace}"
mkdir -p "${workspace}"
cp "${provision_src}" "${workspace}/provision.yaml"

"${bin}" check "${env_name}" --output-dir "${output_dir}"
"${bin}" expand "${env_name}" --output-dir "${output_dir}" --force
go run "${script_dir}/fill_placeholders.go" \
  "${workspace}/full-provision.yaml" \
  "${env_name}" \
  "${metallb_pool}" \
  "${gitops_repo_url}" \
  "${vault_root_token}"
"${bin}" check "${env_name}" --output-dir "${output_dir}"
"${bin}" generate "${env_name}" --output-dir "${output_dir}" --context "${env_name}" --prune
"${bin}" status "${env_name}" --output-dir "${output_dir}"

expected_repos=(
  "basic-infra"
  "basic-infra-${env_name}"
  "support-services"
  "support-services-${env_name}"
  "gitops-controllers"
  "gitops-controllers-${env_name}"
)
for repo in "${expected_repos[@]}"; do
  test -d "${workspace}/${repo}"
done

if [[ "${GITUPS_E2E_APPLY:-true}" == "true" ]]; then
  "${bin}" apply "${env_name}" --output-dir "${output_dir}" --to "${kubectl_context}" --wait-crds
  "${bin}" wait "${env_name}" --output-dir "${output_dir}" --to "${kubectl_context}" --timeout "${wait_timeout}"
  kubectl --context "${kubectl_context}" get namespace \
    olm metallb-system ingress-nginx vault gitea keycloak istio-system argocd declarest-system >/dev/null
fi

printf 'tests/e2e/dsv: completed env=%s output=%s context=%s\n' "${env_name}" "${workspace}" "${kubectl_context}" >&2
