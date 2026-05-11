#!/usr/bin/env bash
set -euo pipefail
# Story 1.5 — gRPC service scaffolder
#
# Architect Round 1 Q1 ruling: Bash + envsubst, POSIX-only, atomic mv into the
# repo from a $(mktemp -d) staging dir, trap cleanup on EXIT/ERR. Refuses to
# overwrite existing services. Atomically appends the new service to go.work
# and infra-lint.yml helm-lint-services matrix (Q8 ruling: auto for go.work +
# infra-lint, manual for build-images.yml).
#
# Usage:
#   scripts/scaffold-svc.sh --name <svc> --domain <domain>
#
# Where:
#   <svc>    matches ^[a-z][a-z0-9-]{1,30}$  (K8s label + Go module + Helm release-name compatible)
#   <domain> matches ^[a-z][a-z0-9]{1,30}$   (proto package segment — no hyphens)

usage() {
  cat >&2 <<EOF
Usage: scripts/scaffold-svc.sh --name <svc> --domain <domain>

  --name <svc>       service name (e.g. auth-svc); regex ^[a-z][a-z0-9-]{1,30}$
  --domain <domain>  proto domain  (e.g. auth);     regex ^[a-z][a-z0-9]{1,30}$

Examples:
  scripts/scaffold-svc.sh --name sample-grpc-app --domain sample
  scripts/scaffold-svc.sh --name auth-svc        --domain auth
EOF
}

die() {
  echo "scaffold-svc: $*" >&2
  exit 1
}

# Repository root = parent of scripts/ dir.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
TPL_ROOT="${SCRIPT_DIR}/scaffold-svc/templates"

# ---- Argument parsing ------------------------------------------------------

NAME=""
DOMAIN=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --name)   NAME="${2:-}"; shift 2 ;;
    --domain) DOMAIN="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage; die "unknown argument: $1" ;;
  esac
done

if [[ -z "${NAME}" || -z "${DOMAIN}" ]]; then
  usage
  die "both --name and --domain are required"
fi

# Q1: argument regex enforcement.
NAME_RE='^[a-z][a-z0-9-]{1,30}$'
DOMAIN_RE='^[a-z][a-z0-9]{1,30}$'
if [[ ! "${NAME}" =~ ${NAME_RE} ]]; then
  usage
  die "--name '${NAME}' must match ${NAME_RE}"
fi
if [[ ! "${DOMAIN}" =~ ${DOMAIN_RE} ]]; then
  usage
  die "--domain '${DOMAIN}' must match ${DOMAIN_RE}"
fi

# Derive PascalCase service name for proto/connect (e.g. domain=auth → Auth).
SVC_PASCAL="$(printf '%s' "${DOMAIN}" | awk '{print toupper(substr($0,1,1)) substr($0,2)}')"
export NAME DOMAIN SVC_PASCAL

# ---- Idempotency precheck (refuse to overwrite) ----------------------------

precheck_path() {
  local target="$1"
  if [[ -e "${target}" ]]; then
    die "service ${NAME} already exists in ${target} — refuse to overwrite"
  fi
}

precheck_path "${REPO_ROOT}/apps/${NAME}"
precheck_path "${REPO_ROOT}/infra/helm/${NAME}"
precheck_path "${REPO_ROOT}/packages/proto/he/${DOMAIN}/v1/${DOMAIN}.proto"

# ---- Staging dir + trap cleanup (Q1 atomicity) -----------------------------

STAGING="$(mktemp -d -t scaffold-svc.XXXXXX)"
cleanup() { rm -rf "${STAGING}"; }
trap cleanup EXIT ERR

# envsubst variable allowlist — restricts substitution to known tokens so any
# unrelated ${VAR} reference (e.g. ${ACR_REGISTRY} in values-staging.yaml) is
# preserved verbatim in the generated output.
ENVSUBST_VARS='$NAME $DOMAIN $SVC_PASCAL'

render_tmpl() {
  local src="$1" dst="$2"
  mkdir -p "$(dirname -- "${dst}")"
  envsubst "${ENVSUBST_VARS}" < "${src}" > "${dst}"
}

# ---- Assemble the 7 target groups in STAGING -------------------------------

# Group 1 — Go service skeleton at apps/${NAME}/.
APP_STAGE="${STAGING}/apps/${NAME}"
render_tmpl "${TPL_ROOT}/cmd/server/main.go.tmpl"        "${APP_STAGE}/cmd/server/main.go"
render_tmpl "${TPL_ROOT}/cmd/server/main_test.go.tmpl"   "${APP_STAGE}/cmd/server/main_test.go"
render_tmpl "${TPL_ROOT}/go.mod.tmpl"                    "${APP_STAGE}/go.mod"
render_tmpl "${TPL_ROOT}/Dockerfile.tmpl"                "${APP_STAGE}/Dockerfile"
render_tmpl "${TPL_ROOT}/turbo.json.tmpl"                "${APP_STAGE}/turbo.json"
render_tmpl "${TPL_ROOT}/package.json.tmpl"              "${APP_STAGE}/package.json"
# m-5: internal/.gitkeep + README hint.
mkdir -p "${APP_STAGE}/internal"
cat > "${APP_STAGE}/internal/.gitkeep" <<EOF
# Business logic goes in apps/${NAME}/internal/; see apps/api-gateway/internal/
# for the mature pattern once it ships.
EOF

# Group 2 — proto contract at packages/proto/he/${DOMAIN}/v1/.
PROTO_STAGE="${STAGING}/packages/proto/he/${DOMAIN}/v1"
render_tmpl "${TPL_ROOT}/proto/svc.proto.tmpl" "${PROTO_STAGE}/${DOMAIN}.proto"

# Group 3 — Helm chart at infra/helm/${NAME}/.
CHART_STAGE="${STAGING}/infra/helm/${NAME}"
render_tmpl "${TPL_ROOT}/helm/Chart.yaml.tmpl"                   "${CHART_STAGE}/Chart.yaml"
render_tmpl "${TPL_ROOT}/helm/values.yaml.tmpl"                  "${CHART_STAGE}/values.yaml"
render_tmpl "${TPL_ROOT}/helm/values-staging.yaml.tmpl"          "${CHART_STAGE}/values-staging.yaml"
render_tmpl "${TPL_ROOT}/helm/templates/deployment.yaml.tmpl"    "${CHART_STAGE}/templates/deployment.yaml"
render_tmpl "${TPL_ROOT}/helm/templates/service.yaml.tmpl"       "${CHART_STAGE}/templates/service.yaml"
render_tmpl "${TPL_ROOT}/helm/templates/serviceaccount.yaml.tmpl" "${CHART_STAGE}/templates/serviceaccount.yaml"
render_tmpl "${TPL_ROOT}/helm/templates/servicemonitor.yaml.tmpl" "${CHART_STAGE}/templates/servicemonitor.yaml"
render_tmpl "${TPL_ROOT}/helm/templates/_helpers.tpl.tmpl"        "${CHART_STAGE}/templates/_helpers.tpl"

# ---- Atomic mv into repo ---------------------------------------------------

mv "${APP_STAGE}"   "${REPO_ROOT}/apps/${NAME}"
mv "${CHART_STAGE}" "${REPO_ROOT}/infra/helm/${NAME}"
mkdir -p "${REPO_ROOT}/packages/proto/he/${DOMAIN}/v1"
mv "${PROTO_STAGE}/${DOMAIN}.proto" "${REPO_ROOT}/packages/proto/he/${DOMAIN}/v1/${DOMAIN}.proto"

# ---- go.work auto-append (Q8 auto, idempotent + parse-validate) ------------

append_go_work() {
  local entry="./apps/${NAME}"
  local gw="${REPO_ROOT}/go.work"
  if grep -qxF "	${entry}" "${gw}"; then
    return 0
  fi
  awk -v entry="	${entry}" '
    /^use \(/ { in_block=1; print; next }
    in_block && /^\)/ { print entry; in_block=0; print; next }
    { print }
  ' "${gw}" > "${gw}.tmp" && mv "${gw}.tmp" "${gw}"
  # Parse-validate the workspace file (Q1 contract).
  (cd "${REPO_ROOT}" && go env GOWORK >/dev/null)
}
append_go_work

# ---- infra-lint.yml helm-lint-services matrix auto-append (Q8 auto) --------

append_infra_lint() {
  local wf="${REPO_ROOT}/.github/workflows/infra-lint.yml"
  if ! grep -q '^  helm-lint-services:' "${wf}"; then
    echo "scaffold-svc: WARN — infra-lint.yml has no helm-lint-services job yet; add it manually (Story 1.5 T7)." >&2
    return 0
  fi
  # Bounded insert: stay within the helm-lint-services job (between its header
  # and the NEXT job header at the same indent) so we don't bleed into
  # helm-lint-observability or any later matrix.
  awk -v name="          - ${NAME}" '
    BEGIN { in_job=0; in_matrix=0; in_chart=0; inserted=0 }
    /^  helm-lint-services:/ { in_job=1; print; next }
    # Leave the job when a sibling job header appears (two-space indent + word + colon).
    in_job && /^  [A-Za-z][A-Za-z0-9_-]*:/ {
      if (in_chart && !inserted) { print name; inserted=1 }
      in_job=0; in_matrix=0; in_chart=0
      print; next
    }
    in_job && /^    strategy:/ { in_matrix=0; in_chart=0 }
    in_job && /^      matrix:/ { in_matrix=1; in_chart=0 }
    in_job && in_matrix && /^        chart:/ { in_chart=1; print; next }
    in_job && in_chart && /^          - / {
      if ($0 == name) { inserted=1 }
      print; next
    }
    # First non-list line after the chart entries inside the job — emit before it.
    in_job && in_chart && /^[^ ]|^    [A-Za-z]|^      [A-Za-z]/ {
      if (!inserted) { print name; inserted=1 }
      in_chart=0
    }
    { print }
    END { if (in_job && in_chart && !inserted) print name }
  ' "${wf}" > "${wf}.tmp" && mv "${wf}.tmp" "${wf}"
}
append_infra_lint

# ---- Vendored proto regen (Q4) ---------------------------------------------

if command -v buf >/dev/null 2>&1; then
  (cd "${REPO_ROOT}/packages/proto" && buf generate) || \
    die "buf generate failed — fix proto + re-run, or remove apps/${NAME}/ to retry"
  git -C "${REPO_ROOT}" add "packages/proto/gen/go/he/${DOMAIN}/v1/" 2>/dev/null || true
else
  echo "scaffold-svc: WARN — 'buf' not on PATH; skipped 'buf generate' (run it manually before pushing)." >&2
fi

# ---- Output manifest + Q8 followup ----------------------------------------

cat <<EOF
✅ Scaffold complete — generated files (atomic mv from ${STAGING}):
  - ${REPO_ROOT}/apps/${NAME}/cmd/server/main.go
  - ${REPO_ROOT}/apps/${NAME}/cmd/server/main_test.go
  - ${REPO_ROOT}/apps/${NAME}/go.mod
  - ${REPO_ROOT}/apps/${NAME}/Dockerfile
  - ${REPO_ROOT}/apps/${NAME}/turbo.json
  - ${REPO_ROOT}/apps/${NAME}/package.json
  - ${REPO_ROOT}/apps/${NAME}/internal/.gitkeep
  - ${REPO_ROOT}/packages/proto/he/${DOMAIN}/v1/${DOMAIN}.proto
  - ${REPO_ROOT}/infra/helm/${NAME}/Chart.yaml
  - ${REPO_ROOT}/infra/helm/${NAME}/values.yaml
  - ${REPO_ROOT}/infra/helm/${NAME}/values-staging.yaml
  - ${REPO_ROOT}/infra/helm/${NAME}/templates/deployment.yaml
  - ${REPO_ROOT}/infra/helm/${NAME}/templates/service.yaml
  - ${REPO_ROOT}/infra/helm/${NAME}/templates/serviceaccount.yaml
  - ${REPO_ROOT}/infra/helm/${NAME}/templates/servicemonitor.yaml
  - ${REPO_ROOT}/infra/helm/${NAME}/templates/_helpers.tpl

Registered (auto):
  - go.work ← ./apps/${NAME}
  - .github/workflows/infra-lint.yml helm-lint-services matrix ← ${NAME}

📋 Followup: add '${NAME}' to .github/workflows/build-images.yml matrix.svc (1 line near line N)
EOF
