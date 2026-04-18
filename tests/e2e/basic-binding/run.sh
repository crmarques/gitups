#!/usr/bin/env bash
# basic-binding e2e case: exercises one capability binding end-to-end.
# argocd consumes the `git-provider` capability from gitea via a single
# binding named `argocd-gitea-bot`. The provider resource fans out across
# every env repo that references support-services.
#
# Env vars:
#   KUBE_CONTEXT              (default: kind-basic-binding)
#   GITUPS_OUTPUT_DIR         (default: gitups-output-dir)
#   METALLB_ADDRESS_POOL      (default: 172.18.255.200-172.18.255.250)
#   GITOPS_REPO_URL           (default: https://example.invalid/gitops/gitops-controllers-dev.git)
#   GITEA_ARGOCD_BOT_TOKEN    (default: e2e-argocd-bot-token)
#   GITUPS_E2E_APPLY          (default: false — no cluster is required for the core assertions)
#   GITUPS_E2E_WAIT_TIMEOUT   (default: 10m)

set -euo pipefail

case_name="basic-binding"
env_key="dev"
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
metallb_pool="${METALLB_ADDRESS_POOL:-172.18.255.200-172.18.255.250}"
gitops_repo_url="${GITOPS_REPO_URL:-https://example.invalid/gitops/gitops-controllers-${env_key}.git}"
gitea_bot_token="${GITEA_ARGOCD_BOT_TOKEN:-e2e-argocd-bot-token}"
wait_timeout="${GITUPS_E2E_WAIT_TIMEOUT:-10m}"

if [[ ! -x "${bin}" ]]; then
  printf 'tests/e2e/basic-binding: %s is missing; run make build first\n' "${bin}" >&2
  exit 1
fi
if [[ ! -f "${provision_src}" ]]; then
  printf 'tests/e2e/basic-binding: provision fixture not found: %s\n' "${provision_src}" >&2
  exit 1
fi

for tool in kubectl helm kustomize go python3; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    printf 'tests/e2e/basic-binding: required tool not found in PATH: %s\n' "${tool}" >&2
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
go run "${script_dir}/fill_placeholders.go" \
  "${workspace}/full-provision.yaml" \
  "${metallb_pool}" \
  "${gitops_repo_url}" \
  "${gitea_bot_token}"

# Re-expand (no --force) to prove idempotency and cross-side binding fill
# propagation. After this run, every placeholder — including the consumer
# side of the git-provider binding — must be resolved.
"${bin}" expand "${case_name}" --output-dir "${output_dir}"

placeholder_count=$(python3 - "${workspace}/full-provision.yaml" <<'PY'
import sys, yaml
with open(sys.argv[1]) as f:
    doc = yaml.safe_load(f)
phs = (doc.get("spec", {}) or {}).get("placeholders") or []
print(len(phs))
PY
)
if [[ "${placeholder_count}" != "0" ]]; then
  printf 'tests/e2e/basic-binding: expected 0 placeholders after cross-side fill, got %s\n' "${placeholder_count}" >&2
  python3 - "${workspace}/full-provision.yaml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
for ph in (doc.get("spec", {}) or {}).get("placeholders") or []:
    print(f"  {ph.get('path')} — {ph.get('reason')}")
PY
  exit 1
fi

"${bin}" check "${case_name}" --output-dir "${output_dir}"
"${bin}" generate "${case_name}" --output-dir "${output_dir}" --context "${case_name}" --prune

expected_repos=(
  "basic-infra"
  "basic-infra-${env_key}"
  "support-services"
  "support-services-${env_key}"
  "gitops-controllers"
  "gitops-controllers-${env_key}"
)
for repo in "${expected_repos[@]}"; do
  if [[ ! -d "${workspace}/${repo}" ]]; then
    printf 'tests/e2e/basic-binding: expected repo dir missing: %s\n' "${repo}" >&2
    exit 1
  fi
done

provider_dir="${workspace}/support-services-${env_key}/packages/gitea/resources/user/argocd-gitea-bot-support-services-${env_key}"
consumer_file="${workspace}/gitops-controllers-${env_key}/packages/argocd/resources/repo-secret/argocd-gitea-bot-support-services-${env_key}/repo-secret.yaml"

for f in configmap-scripts.yaml configmap-values.yaml job-provision.yaml kustomization.yaml; do
  if [[ ! -f "${provider_dir}/${f}" ]]; then
    printf 'tests/e2e/basic-binding: missing provider-side artifact: %s/%s\n' "${provider_dir}" "${f}" >&2
    exit 1
  fi
done

if ! grep -q 'gitups.io/apply-wave' "${provider_dir}/kustomization.yaml"; then
  printf 'tests/e2e/basic-binding: provider kustomization missing apply-wave annotation\n' >&2
  exit 1
fi
if ! grep -q 'gitups.io/readiness' "${provider_dir}/kustomization.yaml"; then
  printf 'tests/e2e/basic-binding: provider kustomization missing readiness annotation\n' >&2
  exit 1
fi

if [[ ! -f "${consumer_file}" ]]; then
  printf 'tests/e2e/basic-binding: missing consumer Secret: %s\n' "${consumer_file}" >&2
  exit 1
fi
if ! grep -q 'argocd.argoproj.io/secret-type: repository' "${consumer_file}"; then
  printf 'tests/e2e/basic-binding: consumer Secret missing argocd repository label\n' >&2
  exit 1
fi
if ! grep -Fq "password: ${gitea_bot_token}" "${consumer_file}"; then
  printf 'tests/e2e/basic-binding: consumer Secret password does not match filled token\n' >&2
  exit 1
fi

provider_wave=$(python3 - "${provider_dir}/kustomization.yaml" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
print(doc["commonAnnotations"]["gitups.io/apply-wave"])
PY
)
consumer_kust="${workspace}/gitops-controllers-${env_key}/packages/argocd/resources/repo-secret/argocd-gitea-bot-support-services-${env_key}/kustomization.yaml"
consumer_wave=$(python3 - "${consumer_kust}" <<'PY'
import sys, yaml
doc = yaml.safe_load(open(sys.argv[1]))
print(doc["commonAnnotations"]["gitups.io/apply-wave"])
PY
)
if [[ "${provider_wave}" -ge "${consumer_wave}" ]]; then
  printf 'tests/e2e/basic-binding: provider wave (%s) must be < consumer wave (%s)\n' \
    "${provider_wave}" "${consumer_wave}" >&2
  exit 1
fi

if grep -r --exclude=full-provision.yaml -l '__GITUPS_PLACEHOLDER__' "${workspace}" >/dev/null 2>&1; then
  printf 'tests/e2e/basic-binding: placeholder sentinel leaked into rendered tree:\n' >&2
  grep -r --exclude=full-provision.yaml -l '__GITUPS_PLACEHOLDER__' "${workspace}" >&2 || true
  exit 1
fi

"${bin}" status "${case_name}" --output-dir "${output_dir}"

if [[ "${GITUPS_E2E_APPLY:-false}" == "true" ]]; then
  "${bin}" apply "${case_name}" --output-dir "${output_dir}" --to "${kubectl_context}" --wait-crds
  "${bin}" wait "${case_name}" --output-dir "${output_dir}" --to "${kubectl_context}" --timeout "${wait_timeout}"
fi

printf 'tests/e2e/basic-binding: completed env=%s output=%s context=%s apply=%s\n' \
  "${env_key}" "${workspace}" "${kubectl_context}" "${GITUPS_E2E_APPLY:-false}" >&2
