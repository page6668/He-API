/**
 * Test implementation for Story 1.2: 搭建 CI/CD 流水线（GitHub Actions）
 *
 * Implemented from the QA-generated skeleton (Turing, 2026-05-11). Every scenario
 * in the design doc has a corresponding `test` here.
 *
 * Static scenarios (Unit) parse the workflow / Dockerfile / package.json with
 * `node:fs` + `yaml` and assert structure.
 *
 * Runtime scenarios (Integration / E2E / runtime-only Blind-Spots) cannot be
 * deterministically reproduced in a local vitest run — they require a real
 * PR / merge run, real ACR/ArgoCD wiring, or runtime side effects. Those are
 * marked `test.skip` and point to evidence in `docs/dev/logs/1.2-dev-log.md`.
 *
 * Test Design: docs/qa/assessments/1.2-test-design-20260511.md
 */

import { describe, expect, test } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYaml } from 'yaml';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const REPO_ROOT = resolve(__dirname, '..', '..', '..');

const readRepoFile = (rel: string): string =>
  readFileSync(resolve(REPO_ROOT, rel), 'utf8');

const loadYaml = <T = unknown>(rel: string): T =>
  parseYaml(readRepoFile(rel)) as T;

const repoFileExists = (rel: string): boolean =>
  existsSync(resolve(REPO_ROOT, rel));

// ---------- workflow shapes (lightweight typings) ----------
interface WorkflowStep {
  id?: string;
  name?: string;
  uses?: string;
  if?: string;
  env?: Record<string, string>;
  with?: Record<string, unknown>;
  run?: string;
}
interface WorkflowJob {
  name?: string;
  'runs-on'?: string;
  if?: string;
  env?: Record<string, string>;
  needs?: string | string[];
  permissions?: Record<string, string> | string;
  steps?: WorkflowStep[];
}
interface Workflow {
  name?: string;
  on?: unknown;
  permissions?: Record<string, string> | string;
  concurrency?: { group?: string; 'cancel-in-progress'?: unknown };
  jobs?: Record<string, WorkflowJob>;
}

const findStep = (
  job: WorkflowJob | undefined,
  predicate: (s: WorkflowStep) => boolean,
): WorkflowStep | undefined => job?.steps?.find(predicate);

// ============================================================
// AC1: PR 自动跑 lint / unit / integration
// ============================================================

describe('AC1: PR 自动跑 lint / unit / integration', () => {
  test('1.2-UNIT-001: .golangci.yml enables required linters + timeout 5m + go 1.22', () => {
    const cfg = loadYaml<{
      run: { timeout: string; go: string };
      linters: { enable: string[] };
    }>('.golangci.yml');
    expect(cfg.run.timeout).toBe('5m');
    expect(cfg.run.go).toBe('1.22');
    for (const linter of ['govet', 'gosimple', 'staticcheck', 'ineffassign', 'errcheck']) {
      expect(cfg.linters.enable).toContain(linter);
    }
  });

  test('1.2-UNIT-002: lint.yml on=pull_request branches=[main] types=[opened,synchronize,reopened]', () => {
    const wf = loadYaml<Workflow>('.github/workflows/lint.yml');
    const on = wf.on as { pull_request: { branches: string[]; types: string[] } };
    expect(on.pull_request.branches).toEqual(['main']);
    expect(on.pull_request.types).toEqual(
      expect.arrayContaining(['opened', 'synchronize', 'reopened']),
    );

    const testWf = loadYaml<Workflow>('.github/workflows/test.yml');
    const onTest = testWf.on as { pull_request: { branches: string[]; types: string[] } };
    expect(onTest.pull_request.branches).toEqual(['main']);
    expect(onTest.pull_request.types).toEqual(
      expect.arrayContaining(['opened', 'synchronize', 'reopened']),
    );
  });

  test('1.2-UNIT-003: lint.yml lint-go uses golangci-lint-action scope ./apps/... (NOT ./packages/...)', () => {
    const wf = loadYaml<Workflow>('.github/workflows/lint.yml');
    const lintGo = wf.jobs?.['lint-go'];
    const golangciStep = findStep(lintGo, (s) => (s.uses ?? '').includes('golangci/golangci-lint-action'));
    expect(golangciStep).toBeDefined();
    const args = String((golangciStep!.with as { args?: string })?.args ?? '');
    expect(args).toContain('--timeout=5m');
    expect(args).toContain('./apps/...');
    expect(args).not.toContain('./packages/...');
  });

  test('1.2-UNIT-004: lint.yml gofumpt scope == ./apps (matches golangci scope)', () => {
    const raw = readRepoFile('.github/workflows/lint.yml');
    expect(raw).toMatch(/gofumpt -l -d \.\/apps\b/);
    expect(raw).not.toMatch(/gofumpt[^\n]+\.\/packages/);
  });

  test('1.2-UNIT-005: test.yml unit-go sets CGO_ENABLED=1 and uses -race', () => {
    const wf = loadYaml<Workflow>('.github/workflows/test.yml');
    const unitGo = wf.jobs?.['unit-go'];
    expect(String(unitGo?.env?.CGO_ENABLED)).toBe('1');
    const raw = readRepoFile('.github/workflows/test.yml');
    expect(raw).toMatch(/go test [^\n]*-race/);
  });

  test('1.2-UNIT-006: test.yml integration job needs=[unit-ts, unit-go] + exit 0 placeholder', () => {
    const wf = loadYaml<Workflow>('.github/workflows/test.yml');
    const integration = wf.jobs?.['integration'];
    expect(integration?.needs).toEqual(expect.arrayContaining(['unit-ts', 'unit-go']));
    const runs = (integration?.steps ?? []).map((s) => s.run ?? '').join('\n');
    expect(runs).toMatch(/exit 0/);
  });

  test('1.2-UNIT-007: build-images.yml concurrency group=build-images-${ref}; cancel-in-progress PR-only', () => {
    const raw = readRepoFile('.github/workflows/build-images.yml');
    expect(raw).toMatch(/concurrency:\s*\n\s+group:\s*build-images-\$\{\{\s*github\.ref\s*\}\}/);
    expect(raw).toMatch(
      /cancel-in-progress:\s*\$\{\{\s*github\.event_name\s*==\s*'pull_request'\s*\}\}/,
    );
  });

  test('1.2-UNIT-008: build-images.yml build-image-pr job: if=pull_request AND push=false', () => {
    const wf = loadYaml<Workflow>('.github/workflows/build-images.yml');
    const prJob = wf.jobs?.['build-image-pr'];
    expect(String(prJob?.if)).toMatch(/github\.event_name == 'pull_request'/);
    const buildStep = findStep(prJob, (s) => (s.uses ?? '').includes('docker/build-push-action'));
    expect(buildStep).toBeDefined();
    expect((buildStep!.with as { push?: boolean }).push).toBe(false);
  });

  test('1.2-UNIT-009: apps/api-gateway/cmd/server/main_test.go exists with ≥1 Test* function', () => {
    const src = readRepoFile('apps/api-gateway/cmd/server/main_test.go');
    expect(src).toMatch(/^func Test[A-Z]\w*\(t \*testing\.T\)/m);
  });

  test('1.2-UNIT-010: shared-types empty.test.ts uses runtime `import * as types` + module assert', () => {
    const src = readRepoFile('packages/shared-types/__tests__/empty.test.ts');
    expect(src).toMatch(/import \* as types/);
    expect(src).toMatch(/expect\(types\)\.toBeDefined\(\)/);
    expect(src).not.toMatch(/\bEmpty\b/);
  });

  test('1.2-UNIT-011: shared-types package.json has vitest devDep + "test": "vitest run"', () => {
    const pkg = JSON.parse(readRepoFile('packages/shared-types/package.json')) as {
      scripts: Record<string, string>;
      devDependencies: Record<string, string>;
    };
    expect(pkg.devDependencies.vitest).toBeDefined();
    expect(pkg.scripts.test).toBe('vitest run');
  });

  // --- Integration (manual / PR-driven) — evidence in dev-log ---

  test.skip('1.2-INT-001: draft PR triggers 6 status checks, all green', () => {
    /* evidence: docs/dev/logs/1.2-dev-log.md §T6 step 1 (PR Run URL) */
  });
  test.skip('1.2-INT-002: inject Go unused-var commit → lint-go red', () => {
    /* evidence: docs/dev/logs/1.2-dev-log.md §T6 step 2 */
  });
  test.skip('1.2-INT-003: inject t.Fatal commit → unit-go red', () => {
    /* evidence: docs/dev/logs/1.2-dev-log.md §T6 step 3 */
  });
  test.skip('1.2-INT-004: inject ESLint violation → lint-ts red', () => {
    /* evidence: docs/dev/logs/1.2-dev-log.md §T6 (symmetric to INT-002) */
  });
  test.skip('1.2-INT-005: second PR push → cache hit → lint+test+build ≤ 60s', () => {
    /* evidence: docs/dev/logs/1.2-dev-log.md §T6 "二次 PR 命中缓存" */
  });
});

// ============================================================
// AC2: merge 自动部署 staging
// ============================================================

describe('AC2: merge 自动部署 staging', () => {
  test('1.2-UNIT-012: build-image-main env.HAS_ACR mapping + push if-gate + fallback step', () => {
    const wf = loadYaml<Workflow>('.github/workflows/build-images.yml');
    const main = wf.jobs?.['build-image-main'];
    expect(String(main?.env?.HAS_ACR)).toMatch(
      /\$\{\{\s*secrets\.ACR_USERNAME\s*!=\s*''\s*&&\s*'true'\s*\|\|\s*'false'\s*\}\}/,
    );
    const pushStep = findStep(main, (s) => /push:\s*true/.test(s.run ?? '') || Boolean((s.with as { push?: boolean })?.push === true));
    expect(pushStep?.if).toMatch(/env\.HAS_ACR\s*==\s*'true'/);
    const fallback = findStep(main, (s) => /HAS_ACR\s*!=\s*'true'/.test(String(s.if ?? '')));
    expect(fallback).toBeDefined();
    expect(String(fallback?.run ?? '')).toMatch(/GITHUB_STEP_SUMMARY/);
  });

  test('1.2-UNIT-013: build-images push tags include only :${github.sha} (NO :staging-latest)', () => {
    const raw = readRepoFile('.github/workflows/build-images.yml');
    expect(raw).toMatch(/he-api\/api-gateway:\$\{\{\s*github\.sha\s*\}\}/);
    expect(raw).not.toContain('staging-latest');
  });

  test('1.2-UNIT-014: image-sha.txt upload step is NOT gated by HAS_ACR (runs unconditionally)', () => {
    const wf = loadYaml<Workflow>('.github/workflows/build-images.yml');
    const main = wf.jobs?.['build-image-main'];
    const upload = findStep(main, (s) => (s.uses ?? '').includes('actions/upload-artifact'));
    expect(upload).toBeDefined();
    expect(String(upload?.if ?? '')).not.toMatch(/HAS_ACR/);
    expect((upload!.with as { name?: string }).name).toBe('image-sha');
  });

  test('1.2-UNIT-015: deploy-staging on=workflow_run + top-level success guard', () => {
    const wf = loadYaml<Workflow>('.github/workflows/deploy-staging.yml');
    const on = wf.on as {
      workflow_run: { workflows: string[]; types: string[]; branches: string[] };
    };
    expect(on.workflow_run.workflows).toEqual(['build-images']);
    expect(on.workflow_run.types).toEqual(['completed']);
    expect(on.workflow_run.branches).toEqual(['main']);
    for (const job of Object.values(wf.jobs ?? {})) {
      expect(String(job.if ?? '')).toMatch(/workflow_run\.conclusion\s*==\s*'success'/);
    }
  });

  test('1.2-UNIT-016: deploy-staging concurrency group=deploy-staging cancel-in-progress=false', () => {
    const wf = loadYaml<Workflow>('.github/workflows/deploy-staging.yml');
    expect(wf.concurrency?.group).toBe('deploy-staging');
    expect(wf.concurrency?.['cancel-in-progress']).toBe(false);
  });

  test('1.2-UNIT-017: deploy-staging job permissions: contents=write AND actions=read', () => {
    const wf = loadYaml<Workflow>('.github/workflows/deploy-staging.yml');
    const updateJob = wf.jobs?.['update-helm-values'];
    const perms = updateJob?.permissions as Record<string, string>;
    expect(perms.contents).toBe('write');
    expect(perms.actions).toBe('read');
  });

  test('1.2-UNIT-018: probe_helm step AFTER actions/checkout; gating via steps.probe_helm.outputs.exists', () => {
    const wf = loadYaml<Workflow>('.github/workflows/deploy-staging.yml');
    const job = wf.jobs?.['update-helm-values'];
    const steps = job?.steps ?? [];
    const checkoutIdx = steps.findIndex((s) => (s.uses ?? '').includes('actions/checkout'));
    const probeIdx = steps.findIndex((s) => s.id === 'probe_helm');
    expect(checkoutIdx).toBeGreaterThanOrEqual(0);
    expect(probeIdx).toBeGreaterThan(checkoutIdx);

    const updateStep = steps.find((s) =>
      String(s.if ?? '').includes("steps.probe_helm.outputs.exists == 'true'"),
    );
    expect(updateStep).toBeDefined();

    // M-new-1 regression guard: no job-level env mapping that uses hashFiles for HAS_HELM_VALUES.
    const jobEnv = job?.env ?? {};
    for (const [k, v] of Object.entries(jobEnv)) {
      expect(k).not.toBe('HAS_HELM_VALUES');
      expect(String(v)).not.toMatch(/hashFiles\(/);
    }
  });

  test('1.2-UNIT-019: download-artifact uses run-id=workflow_run.id + github-token', () => {
    const wf = loadYaml<Workflow>('.github/workflows/deploy-staging.yml');
    const job = wf.jobs?.['update-helm-values'];
    const dl = findStep(job, (s) => (s.uses ?? '').includes('actions/download-artifact'));
    expect(dl).toBeDefined();
    const w = dl!.with as { name?: string; 'run-id'?: string; 'github-token'?: string };
    expect(w.name).toBe('image-sha');
    expect(String(w['run-id'])).toMatch(/github\.event\.workflow_run\.id/);
    expect(String(w['github-token'])).toMatch(/secrets\.GITHUB_TOKEN/);
  });

  test('1.2-UNIT-020: Dockerfile multi-stage golang:1.22-alpine→distroless + CGO_ENABLED=0 + entrypoint', () => {
    const df = readRepoFile('apps/api-gateway/Dockerfile');
    expect(df).toContain('FROM golang:1.22-alpine');
    expect(df).toContain('FROM gcr.io/distroless/static-debian12');
    expect(df).toMatch(/ENV CGO_ENABLED=0/);
    expect(df).toMatch(/ENTRYPOINT \["\/gateway"\]/);
  });

  // --- E2E (manual / merge-driven) ---

  test.skip('1.2-E2E-001: merge with secrets absent → build-images main run gated-skip + artifact uploaded + exit 0', () => {
    /* evidence: docs/dev/logs/1.2-dev-log.md §T6 step 4.1 */
  });
  test.skip('1.2-E2E-002: deploy-staging triggered by workflow_run; probe_helm.exists=false + HAS_ARGOCD=false → all gated skip + exit 0', () => {
    /* evidence: docs/dev/logs/1.2-dev-log.md §T6 step 4.2 */
  });
  test.skip('1.2-E2E-003: deploy-staging step summary contains image SHA + every gating reason', () => {
    /* evidence: docs/dev/logs/1.2-dev-log.md §T6 step 4.3 */
  });

  // --- Blind Spot Scenarios ---

  test('[BLIND-SPOT] 1.2-BLIND-BOUNDARY-001: empty secrets.ACR_USERNAME → HAS_ACR=false', () => {
    const wf = loadYaml<Workflow>('.github/workflows/build-images.yml');
    const env = wf.jobs?.['build-image-main']?.env ?? {};
    expect(String(env.HAS_ACR)).toMatch(
      /\$\{\{\s*secrets\.ACR_USERNAME\s*!=\s*''\s*&&\s*'true'\s*\|\|\s*'false'\s*\}\}/,
    );
  });

  test('[BLIND-SPOT] 1.2-BLIND-BOUNDARY-002: empty vars.ARGOCD_ENDPOINT → HAS_ARGOCD=false', () => {
    const wf = loadYaml<Workflow>('.github/workflows/deploy-staging.yml');
    const env = wf.jobs?.['trigger-argocd-sync']?.env ?? {};
    expect(String(env.HAS_ARGOCD)).toMatch(
      /\$\{\{\s*vars\.ARGOCD_ENDPOINT\s*!=\s*''\s*&&\s*'true'\s*\|\|\s*'false'\s*\}\}/,
    );
  });

  test('[BLIND-SPOT] 1.2-BLIND-BOUNDARY-003: empty/missing image-sha.txt → resolve step fails visibly', () => {
    const wf = loadYaml<Workflow>('.github/workflows/deploy-staging.yml');
    const job = wf.jobs?.['update-helm-values'];
    const resolveStep = findStep(job, (s) =>
      String(s.run ?? '').includes('image-sha.txt'),
    );
    expect(resolveStep).toBeDefined();
    expect(String(resolveStep?.run)).toMatch(/-s image-sha\.txt[\s\S]*exit 1/);
  });

  test('[BLIND-SPOT] 1.2-BLIND-BOUNDARY-004: golangci-lint scope drift to ./packages/... → "no Go files"', () => {
    const raw = readRepoFile('.github/workflows/lint.yml');
    expect(raw).toContain('./apps/...');
    expect(raw).not.toContain('./packages/...');
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-ERROR-001: ACR push failure → workflow_run.conclusion=failure → deploy-staging guard skips', () => {
    /* statically covered by 1.2-UNIT-015; runtime verification deferred to Story 1.3 dev-log */
  });

  test('[BLIND-SPOT] 1.2-BLIND-ERROR-002: artifact expired >7d → download-artifact fails → deploy-staging fails', () => {
    const wf = loadYaml<Workflow>('.github/workflows/build-images.yml');
    const main = wf.jobs?.['build-image-main'];
    const upload = findStep(main, (s) => (s.uses ?? '').includes('actions/upload-artifact'));
    expect((upload?.with as { 'retention-days'?: number })?.['retention-days']).toBe(7);
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-ERROR-003: malformed values-staging.yaml → yq fails non-0', () => {
    /* static check not possible until Story 1.3 creates values-staging.yaml */
  });

  test('[BLIND-SPOT] 1.2-BLIND-ERROR-004: branch protection rejects bot push → git push fails visibly', () => {
    // Static guard: README must describe github-actions[bot] bypass requirement.
    const readme = readRepoFile('README.md');
    expect(readme).toMatch(/github-actions\[bot\][\s\S]{0,200}Bypass/i);
    expect(readme).toMatch(/静默断链|静默拒绝/);
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-ERROR-005: build-images conclusion=failure → all deploy-staging jobs skipped', () => {
    /* statically covered by 1.2-UNIT-015; runtime verification deferred */
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-FLOW-001: parallel main merges → deploy-staging serialized via concurrency', () => {
    /* statically covered by 1.2-UNIT-016; runtime verification needs Story 1.3 follow-up */
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-FLOW-002: PR synchronize cancels previous in-flight PR build-images run', () => {
    /* statically covered by 1.2-UNIT-007; runtime verification deferred to dev-log T6 */
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-FLOW-003: main push does NOT cancel in-flight main build-images run', () => {
    /* statically covered by 1.2-UNIT-007 (cancel-in-progress gated on pull_request) */
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-CONCURRENCY-001: sequential deploy-staging runs fast-forward cleanly', () => {
    /* Story 1.3 follow-up: needs real helm values + 2 sequential merges */
  });

  test('[BLIND-SPOT] 1.2-BLIND-CONCURRENCY-002: download-artifact pinned by workflow_run.id avoids race', () => {
    // identical assertion to 1.2-UNIT-019; kept for traceability per design doc.
    const wf = loadYaml<Workflow>('.github/workflows/deploy-staging.yml');
    const job = wf.jobs?.['update-helm-values'];
    const dl = findStep(job, (s) => (s.uses ?? '').includes('actions/download-artifact'));
    expect(String((dl?.with as { 'run-id'?: string })?.['run-id'])).toMatch(
      /github\.event\.workflow_run\.id/,
    );
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-DATA-001: GITHUB_TOKEN-authored commit does NOT re-trigger workflows', () => {
    /* Story 1.3 follow-up — first real helm-values push must not produce a second deploy-staging run */
  });

  test.skip('[BLIND-SPOT] 1.2-BLIND-DATA-002: 3-way SHA consistency (artifact == ACR tag == helm values)', () => {
    /* Story 1.3 follow-up — compare step summary SHAs across build-images / ACR / values-staging diff */
  });

  test('[BLIND-SPOT] 1.2-BLIND-RESOURCE-001: buildx gha cache bounded by GitHub eviction (no manual cleanup)', () => {
    const raw = readRepoFile('.github/workflows/build-images.yml');
    expect(raw).toMatch(/cache-to:\s*type=gha,mode=max/);
  });

  test('[BLIND-SPOT] 1.2-BLIND-RESOURCE-002: image-sha artifact retention-days=7 (auto-cleaned)', () => {
    const wf = loadYaml<Workflow>('.github/workflows/build-images.yml');
    const main = wf.jobs?.['build-image-main'];
    const upload = findStep(main, (s) => (s.uses ?? '').includes('actions/upload-artifact'));
    expect((upload?.with as { 'retention-days'?: number })?.['retention-days']).toBe(7);
  });
});

// ============================================================
// Sanity: deliverable_bindings can locate every artifact
// ============================================================

describe('Deliverable presence', () => {
  test.each([
    '.github/workflows/lint.yml',
    '.github/workflows/test.yml',
    '.github/workflows/build-images.yml',
    '.github/workflows/deploy-staging.yml',
    '.golangci.yml',
    'apps/api-gateway/Dockerfile',
    'apps/api-gateway/cmd/server/main_test.go',
    'packages/shared-types/__tests__/empty.test.ts',
    'packages/shared-types/vitest.config.ts',
    'scripts/ci/setup-toolchain.sh',
  ])('%s exists', (rel) => {
    expect(repoFileExists(rel)).toBe(true);
  });
});
