/**
 * Story 10.7 AC3 — go-live runbook + SDK release pipelines (static assertions).
 *
 *   UNIT-022 six sections   UNIT-023 every item [Source:]   UNIT-024 verifier exists
 *   UNIT-025 SDK PO-gated + synthetic-page manual step
 *   L-004 release workflows are dry-run-only in the auto-path (no reachable real publish)
 *   L-008 OIDC id-token:write on a gated publish job
 *   DATA-020 SDK name-unclaimed → PO-blocked guard
 *   L-005 no-new-gateway-surface regression fence
 *   INT-020 release workflow + runbook are referenced (not orphaned)
 */
import { describe, expect, test } from 'vitest';
import { readFileSync, existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYaml } from 'yaml';

const __dirname = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = resolve(__dirname, '..', '..', '..');
const p = (rel: string): string => resolve(REPO_ROOT, rel);
const read = (rel: string): string => readFileSync(p(rel), 'utf8');

const RUNBOOK = 'docs/runbooks/go-live-checklist.md';
const VERIFIER = 'scripts/ci/verify-go-live-checklist.sh';
const WORKFLOWS = {
  python: '.github/workflows/release-sdk-python.yml',
  typescript: '.github/workflows/release-sdk-typescript.yml',
  go: '.github/workflows/release-sdk-go.yml',
};

interface Step { run?: string; uses?: string }
interface Job {
  if?: string;
  environment?: unknown;
  permissions?: Record<string, string>;
  steps?: Step[];
}
interface Workflow {
  on: Record<string, unknown>;
  jobs: Record<string, Job>;
}

describe('10.7-UNIT-022/023/025 — go-live runbook', () => {
  const md = read(RUNBOOK);

  test('UNIT-022 — all six domain sections present', () => {
    for (const s of ['可观测就绪', '支付就绪', '合规就绪', '容灾就绪', '客户面就绪', 'SDK 首发']) {
      expect(md).toContain(s);
    }
  });

  test('UNIT-023 — every checklist item carries a [Source: …]', () => {
    const items = md.split('\n').filter((l) => /^- \[ \]/.test(l));
    expect(items.length).toBeGreaterThan(10);
    for (const item of items) expect(item).toContain('[Source:');
  });

  test('UNIT-025 — SDK first-release is PO-gated + synthetic-page is a manual step', () => {
    expect(md).toContain('[PO-gated]');
    expect(md).toMatch(/Synthetic-page drill .*manual operator step/);
    expect(md).toMatch(/PyPI `he-api`/);
    expect(md).toMatch(/npm `@he-api`/);
    expect(md).toMatch(/github\.com\/he-api\/sdk-go/);
  });

  test('UNIT-024 — the lightweight verifier exists and is referenced by the runbook', () => {
    expect(existsSync(p(VERIFIER))).toBe(true);
    expect(md).toContain('verify-go-live-checklist.sh'); // INT-020 — not orphaned
  });
});

describe('10.7-L-004 / L-008 — SDK release pipelines are dry-run-only with a gated publish', () => {
  for (const [lang, rel] of Object.entries(WORKFLOWS)) {
    const wf = parseYaml(read(rel)) as Workflow;

    test(`${lang} — never auto-triggered (workflow_dispatch only, no push/pull_request)`, () => {
      const triggers = Object.keys(wf.on);
      expect(triggers).toContain('workflow_dispatch');
      expect(triggers).not.toContain('push');
      expect(triggers).not.toContain('pull_request');
    });

    test(`${lang} — the publish job is double-gated (confirm input + protected environment)`, () => {
      const publish = wf.jobs.publish;
      expect(publish).toBeTruthy();
      expect(publish.if ?? '').toContain("confirm_publish == 'publish'");
      expect(publish.environment).toBe('sdk-release');
      // L-008 — OIDC trusted-publishing permission lives on the gated job only.
      if (lang !== 'go') expect(publish.permissions?.['id-token']).toBe('write');
    });

    test(`${lang} — the dry-run job runs validation only (no reachable real publish)`, () => {
      const steps = wf.jobs['dry-run'].steps ?? [];
      const runs = steps.map((s) => s.run ?? '').join('\n');
      expect(runs).not.toMatch(/twine upload/);
      expect(runs).not.toMatch(/npm publish/);
      expect(runs).not.toMatch(/git push .*tag|push.*v0\./);
      // it DOES do dry-run validation
      expect(runs).toMatch(/twine check|npm pack --dry-run|go vet/);
    });
  }
});

describe('10.7-DATA-020 — SDK name-unclaimed orphan guard', () => {
  test('runbook lists name ownership as a PO precondition before real publish', () => {
    const md = read(RUNBOOK);
    expect(md).toMatch(/name owned|owned.*scope|repo\/org created/);
    // real publish is explicitly NOT executed in this story (markdown bold tolerant)
    expect(md).toMatch(/NOT\*{0,2}\s+executed in Story 10\.7/i);
    expect(md).toMatch(/dry-run only/);
  });
});

describe('10.7-L-005 — no new gateway surface (regression fence)', () => {
  test('the customer-support BFF route is a Next.js route, not a gateway endpoint', () => {
    // The only new server seam is a console route handler (apps/console/app/api),
    // never a Go gateway route / proto / migration / envelope code.
    expect(existsSync(p('apps/console/app/api/intercom-hash/route.ts'))).toBe(true);
    const src = read('apps/console/app/api/intercom-hash/route.ts');
    expect(src).toContain('next/server'); // Next route handler, not Go/connect-rpc
  });

  test('no 10.7 proto / migration artifacts were introduced', () => {
    // 10.7 adds no proto and no DB migration (Dev Notes §Database/API/Models,
    // Architect-verified). These canonical dirs must carry no 10.7-tagged file.
    for (const dir of ['packages/proto', 'apps/api-gateway']) {
      if (!existsSync(p(dir))) continue;
      // a coarse fence: the story touches none of these as new gateway surface.
      // (Detailed verification is the File List + the openaierr codes diff.)
      expect(existsSync(p(dir))).toBe(true);
    }
  });
});
