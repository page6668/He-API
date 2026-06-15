/**
 * Story 10.5: 文档站（Quickstart + API Reference + Cookbook，10 种语言）— implemented.
 *
 * Skeleton authored by QA Test Design (Turing, 2026-06-15); every scenario ID below maps to
 *   docs/qa/assessments/10.5-test-design-20260615.md
 * and is now implemented by Dev (Linus). Framework: Docusaurus 3.x (Architect OQ-10.5-1).
 *
 * Coverage split (per skeleton's allowance to relocate pure build-gate assertions):
 *   - File / config / gate-script assertions run in Node (build-time gates: build, link-check,
 *     anti-drift, completeness, provenance) — they assert the same invariants the CI gates enforce.
 *   - Browser E2E (Playwright) assertions drive the served static site (home, sections, RTL,
 *     language switch, fallback).
 *
 * Gated: 10.5-INT-007 (Go-SDK content) stays `test.skip` until Story 10.4 reaches Done
 *   (Architect High Issue); Python + TS are Done on disk and safe to assert now.
 */

import { test, expect } from '@playwright/test';
import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync, mkdtempSync, cpSync, rmSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';

const LOCALES = ['en', 'zh-CN', 'ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar'] as const;
const SECTIONS = ['quickstart', 'api-reference', 'cookbook'] as const;

const DOCS_ROOT = resolve(__dirname, '..');
const REPO_ROOT = resolve(DOCS_ROOT, '..', '..');
const BUILD_DIR = join(DOCS_ROOT, 'build');
const DOCS_SRC = join(DOCS_ROOT, 'docs');

function tsx(script: string, args: string[] = []): { code: number; out: string } {
  // spawnSync (not execFileSync) so we capture BOTH stdout and stderr regardless of exit code —
  // the gates print warnings to stderr and structural errors to stderr too.
  const r = spawnSync('pnpm', ['exec', 'tsx', join('scripts', script), ...args], {
    cwd: DOCS_ROOT,
    encoding: 'utf8',
  });
  return { code: r.status ?? 1, out: `${r.stdout ?? ''}${r.stderr ?? ''}` };
}

function readDoc(rel: string): string {
  return readFileSync(join(DOCS_SRC, rel), 'utf8');
}

// ============================================================
// AC1: docs.he-api.com 可访问 — buildable/deployable public docs site
// ============================================================

test.describe('AC1: 可构建/可部署的公开文档站 (Quickstart + API Reference + Cookbook + SDK 索引)', () => {
  test('10.5-INT-001: docs build emitted a static artifact (build exits 0 in CI turbo `build`)', () => {
    // P0 | integration. The e2e suite depends on a completed `docusaurus build` (turbo
    // test:e2e dependsOn build); a non-zero build would have failed CI before this runs.
    expect(existsSync(join(BUILD_DIR, 'index.html')), 'build/index.html (en SSG output)').toBe(true);
    expect(existsSync(join(BUILD_DIR, 'sitemap.xml'))).toBe(true);
    for (const loc of LOCALES) {
      const home = loc === 'en' ? join(BUILD_DIR, 'index.html') : join(BUILD_DIR, loc, 'index.html');
      expect(existsSync(home), `static home for ${loc}`).toBe(true);
    }
  });

  test('10.5-INT-002: link-check is enforced (onBrokenLinks/onBrokenAnchors = throw) and the build passed', () => {
    // P0 | integration. Docusaurus link-check IS the build gate: a dead internal link or anchor
    // throws and fails `build`. We assert the gate is armed AND a clean artifact exists.
    const cfg = readFileSync(join(DOCS_ROOT, 'docusaurus.config.ts'), 'utf8');
    expect(cfg).toMatch(/onBrokenLinks:\s*'throw'/);
    expect(cfg).toMatch(/onBrokenAnchors:\s*'throw'/);
    expect(cfg).toMatch(/onBrokenMarkdownLinks:\s*'throw'/);
    expect(existsSync(join(BUILD_DIR, 'index.html'))).toBe(true);
  });

  test('10.5-INT-003: apps/docs in pnpm `apps/*` glob AND build/lint in turbo graph (anti-orphan)', () => {
    // P0 | integration (config_read)
    const ws = readFileSync(join(REPO_ROOT, 'pnpm-workspace.yaml'), 'utf8');
    expect(ws).toMatch(/apps\/\*/);
    const pkg = JSON.parse(readFileSync(join(DOCS_ROOT, 'package.json'), 'utf8'));
    expect(pkg.name).toBe('@he-api/docs');
    expect(pkg.private).toBe(true);
    expect(pkg.scripts.build).toBeTruthy();
    expect(pkg.scripts.lint).toBeTruthy();
    // turbo has generic build+lint tasks that apply to every workspace package.
    const turbo = JSON.parse(readFileSync(join(REPO_ROOT, 'turbo.json'), 'utf8'));
    expect(turbo.tasks.build).toBeTruthy();
    expect(turbo.tasks.lint).toBeTruthy();
  });

  test('10.5-INT-004: API Reference endpoint set == rest-api-spec.md §5.1 (anti-drift)', () => {
    // P0 | integration — backed by scripts/check-api-ref-drift.ts (endpoint equality vs spec).
    const ref = readDoc('api-reference.mdx');
    for (const ep of [
      '/v1/chat/completions',
      '/v1/embeddings',
      '/v1/audio/transcriptions',
      '/v1/audio/speech',
      '/v1/models',
      '/v1/usage',
      '/v1/balance',
    ]) {
      expect(ref, `endpoint ${ep} documented`).toContain(ep);
    }
    expect(ref).toContain('data: [DONE]'); // SSE terminator
    const drift = tsx('check-api-ref-drift.ts');
    expect(drift.out + '').toContain('anti-drift gate passed');
    expect(drift.code).toBe(0);
  });

  test('10.5-INT-005: API Reference error-code table == rest-api-spec §5.1.2 (anti-drift)', () => {
    // P0 | integration
    const ref = readDoc('api-reference.mdx');
    for (const code of [
      '400_invalid_request',
      '401_invalid_api_key',
      '402_balance_insufficient',
      '402_quota_exhausted',
      '403_ip_not_whitelisted',
      '403_model_not_in_scope',
      '429_rate_limit_qps',
      '429_rate_limit_rpm',
      '429_rate_limit_tpm',
      '502_upstream_unavailable',
      '504_upstream_timeout',
    ]) {
      expect(ref, `error code ${code} documented`).toContain(code);
    }
  });

  test('10.5-INT-006: response headers documented incl. X-He-Selected-Model NOT set on A/B path', () => {
    // P0 | integration
    const ref = readDoc('api-reference.mdx');
    expect(ref).toContain('X-He-Request-Id');
    expect(ref).toContain('X-He-Selected-Model');
    expect(ref).toContain('X-He-Cost-Usd');
    expect(ref).toMatch(/X-He-Selected-Model[`*\s]+is[`*\s]+NOT[`*\s]+set/i);
  });

  test('10.5-UNIT-001: SDK-index install commands == ratified facts (pip install he-api / npm install @he-api/sdk)', () => {
    // P0 | unit. Python (he-api) + TS (@he-api/sdk) are Done on disk.
    const sdk = readDoc('sdk/index.mdx');
    expect(sdk).toContain('pip install he-api');
    expect(sdk).toContain('npm install @he-api/sdk');
  });

  test('10.5-E2E-001: en default home (/) returns 200', async ({ page }) => {
    // P0 | e2e
    const resp = await page.goto('/');
    expect(resp?.status()).toBe(200);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  });

  test('10.5-E2E-002: three sections reachable + navigable (/quickstart /api-reference /cookbook)', async ({ page }) => {
    // P0 | e2e
    for (const section of SECTIONS) {
      const resp = await page.goto(`/${section}`);
      expect(resp?.status(), `${section} status`).toBe(200);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    }
  });

  test('10.5-E2E-003: docs pages load without login/auth (public)', async ({ page }) => {
    // P1 | e2e | front-end-spec D-7. Static site → no auth gate.
    await page.context().clearCookies();
    const resp = await page.goto('/quickstart');
    expect(resp?.status()).toBe(200);
    expect(page.url()).not.toContain('login');
  });

  test.skip('10.5-INT-007: SDK index Go entry (go get .../sdk-go) — BLOCKED until Story 10.4 Done', () => {
    // P1 | integration. GATE: docs/stories/10.4-go-sdk.md status == "Approved" (NOT Done).
    // Un-skip when 10.4 reaches Done; then assert `go get github.com/he-api/sdk-go` + heapi.NewClient
    // match the ratified module path/surface. Reconcile at T5.2. Go content currently ships as
    // PROVISIONAL (Architect High Issue option b).
    const sdk = readDoc('sdk/index.mdx');
    expect(sdk).toContain('go get github.com/he-api/sdk-go');
  });

  test('10.5-INT-008: Quickstart contains ≤5-min path (register → API key → first chat.completions + base_url/HE_API_KEY + migration)', () => {
    // P1 | integration (content presence)
    const qs = readDoc('quickstart.mdx');
    expect(qs).toContain('console.he-api.com'); // register / create key
    expect(qs).toContain('HE_API_KEY');
    expect(qs).toContain('base_url');
    expect(qs).toContain('chat.completions');
    expect(qs).toMatch(/Migrating from OpenAI/i);
  });

  test('10.5-INT-009: Cookbook contains task examples (streaming / embeddings / multimodal·audio / routing·A-B / usage)', () => {
    // P1 | integration (content presence)
    const cb = readDoc('cookbook.mdx');
    expect(cb).toMatch(/stream/i);
    expect(cb).toContain('embeddings');
    expect(cb).toMatch(/audio\.(transcriptions|speech)/);
    expect(cb).toContain('X-He-AB-Models'); // routing / A-B
    expect(cb).toContain('/v1/usage');
  });

  test('10.5-E2E-004: SDK-index page reachable + lists pip/npm/go commands', async ({ page }) => {
    // P1 | e2e
    const resp = await page.goto('/sdk');
    expect(resp?.status()).toBe(200);
    const body = await page.locator('main').innerText();
    expect(body).toContain('pip install he-api');
    expect(body).toContain('npm install @he-api/sdk');
    expect(body).toContain('go get github.com/he-api/sdk-go');
  });

  test('10.5-INT-010: `pnpm --filter @he-api/docs lint` GREEN (completeness + anti-drift gates)', () => {
    // P2 | integration
    const r = tsx('check-docs-completeness.ts');
    expect(r.code, r.out).toBe(0);
    const d = tsx('check-api-ref-drift.ts');
    expect(d.code, d.out).toBe(0);
  });

  test('10.5-E2E-005: static assets served cacheable (NOT no-store)', async ({ page }) => {
    // P2 | e2e | BR-10.5.5 (distinct from gateway no-store)
    const resp = await page.goto('/');
    const cc = resp?.headers()['cache-control'] ?? '';
    expect(cc).not.toContain('no-store');
  });

  test('10.5-INT-011: /errors folded into API Reference + /migration folded into Quickstart present', () => {
    // P2 | integration | OQ-10.5-6 (absorbed not cut)
    const ref = readDoc('api-reference.mdx');
    expect(ref).toMatch(/##\s*Error codes/i); // /errors folded in
    const qs = readDoc('quickstart.mdx');
    expect(qs).toMatch(/Migrating from OpenAI/i); // /migration folded in
  });

  test('10.5-E2E-006: mobile-readable / responsive + a11y basics (skip link, lang attr)', async ({ page }) => {
    // P2 | e2e | front-end-spec §7 WCAG / §8
    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto('/');
    expect(await page.locator('[class*="skipToContent"]').count()).toBeGreaterThan(0);
    expect(await page.locator('html').getAttribute('lang')).toBe('en');
  });

  // --- Blind Spot Scenarios ---

  test('[BLIND-SPOT] 10.5-BLIND-ERROR-001: a broken internal link FAILS the build gate (negative)', () => {
    // ERROR | P1 | ERROR-003. Add a throwaway en doc with a known-dead link, then build en in the
    // REAL workspace (deps present, separate --out-dir so the served build/ is untouched). The only
    // failure cause is the broken link → proves onBrokenLinks:'throw' actually bites. Then a clean
    // re-add WITHOUT the bad link is NOT needed — we just remove the throwaway file.
    test.setTimeout(180_000);
    // NOTE: filename must NOT start with `_` — Docusaurus treats underscore-prefixed files as
    // excluded partials, which would skip the link check entirely.
    const brokenDoc = join(DOCS_SRC, 'zz-blind-error-probe.mdx');
    const outDir = 'build-blind-error-001';
    try {
      writeFileSync(
        brokenDoc,
        `---\nid: zz-blind-error-probe\ntitle: Broken Link Probe\n---\n\n# Probe\n\n[dead](/this-page-does-not-exist-xyz)\n`,
      );
      const r = spawnSync('pnpm', ['exec', 'docusaurus', 'build', '--locale', 'en', '--out-dir', outDir], {
        cwd: DOCS_ROOT,
        encoding: 'utf8',
      });
      expect(r.status, 'broken internal link must fail the build gate').not.toBe(0);
      expect(`${r.stdout}${r.stderr}`).toMatch(/[Bb]roken link/);
    } finally {
      rmSync(brokenDoc, { force: true });
      rmSync(join(DOCS_ROOT, outDir), { recursive: true, force: true });
    }
  });

  test('[BLIND-SPOT] 10.5-BLIND-FLOW-001: direct-URL deep link to a section page (SSG) works without client nav', async ({ page }) => {
    // FLOW | P2 | FLOW-005
    const resp = await page.goto('/cookbook');
    expect(resp?.status()).toBe(200);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  });
});

// ============================================================
// AC2: 10 种语言齐全 — Arabic RTL, switch without losing context
// ============================================================

test.describe('AC2: 10 种语言齐全 + RTL + 切换不丢上下文', () => {
  test('10.5-UNIT-002: completeness-gate logic detects a deliberate gap (fixture)', () => {
    // P0 | unit | mirrors scripts/check-i18n-keys.ts (doc-side = page/section)
    const tmp = mkdtempSync(join(tmpdir(), 'he-docs-gap-'));
    try {
      cpSync(join(DOCS_ROOT, 'docusaurus.config.ts'), join(tmp, 'docusaurus.config.ts'));
      cpSync(DOCS_SRC, join(tmp, 'docs'), { recursive: true });
      rmSync(join(tmp, 'docs', 'cookbook.mdx')); // deliberate structural gap
      const r = tsx('check-docs-completeness.ts', ['--root', tmp]);
      expect(r.code).toBe(1);
      expect(r.out).toMatch(/BLOCK: required section "cookbook"/);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  });

  test('10.5-UNIT-003: locale set is exactly the 10 (en/zh-CN/ja/ko/es/fr/de/pt/ru/ar)', () => {
    // P0 | unit | BR-10.5.7 (no zh-TW/hi/id)
    const cfg = readFileSync(join(DOCS_ROOT, 'docusaurus.config.ts'), 'utf8');
    const m = cfg.match(/const\s+LOCALES\s*=\s*\[([^\]]*)\]/);
    expect(m).toBeTruthy();
    const locales = [...(m![1]).matchAll(/'([^']+)'/g)].map((x) => x[1]);
    expect(locales.sort()).toEqual([...LOCALES].sort());
    // The gate also enforces this:
    const r = tsx('check-docs-completeness.ts');
    expect(r.out).toMatch(/locales:.*\(10\) ✓/);
  });

  test('10.5-INT-012: completeness gate is BLOCKING (build-gate FAIL) on a hard-missing page/section', () => {
    // P0 | integration | OQ-10.5-4 (structural gap = BLOCK)
    const tmp = mkdtempSync(join(tmpdir(), 'he-docs-block-'));
    try {
      cpSync(join(DOCS_ROOT, 'docusaurus.config.ts'), join(tmp, 'docusaurus.config.ts'));
      cpSync(DOCS_SRC, join(tmp, 'docs'), { recursive: true });
      rmSync(join(tmp, 'docs', 'api-reference.mdx'));
      const r = tsx('check-docs-completeness.ts', ['--root', tmp]);
      expect(r.code).toBe(1);
      expect(r.out).toMatch(/FAILED/);
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  });

  test('10.5-INT-013: missing-translation page falls back to en (same page), not blank/404', () => {
    // P0 | integration | FR-10.3 + BR-10.5.8. sdk/python is intentionally not translated → fallback.
    expect(existsSync(join(BUILD_DIR, 'zh-CN', 'sdk', 'python', 'index.html')), 'zh-CN fallback build').toBe(true);
    expect(existsSync(join(BUILD_DIR, 'ar', 'sdk', 'python', 'index.html')), 'ar fallback build').toBe(true);
  });

  test('10.5-E2E-007: ar page renders dir="rtl" + mirrored layout', async ({ page }) => {
    // P0 | e2e | highest-risk locale, in-scope proofing (OQ-4)
    await page.goto('/ar/quickstart');
    expect(await page.locator('html').getAttribute('dir')).toBe('rtl');
    expect(await page.locator('html').getAttribute('lang')).toBe('ar');
  });

  test('10.5-E2E-008: ar code/paths/package-names/commands stay LTR (not flipped)', async ({ page }) => {
    // P0 | e2e | BR-10.5.10 LTR-island — Architect top-weighted risk
    await page.goto('/ar/api-reference');
    expect(await page.locator('html').getAttribute('dir')).toBe('rtl');
    const pre = page.locator('pre').first();
    await expect(pre).toBeVisible(); // ensure layout + stylesheet applied before reading computed style
    const preDir = await pre.evaluate((el) => getComputedStyle(el).direction);
    expect(preDir, 'code block stays LTR under RTL').toBe('ltr');
    const code = page.locator('code').first();
    await expect(code).toBeVisible();
    const codeDir = await code.evaluate((el) => getComputedStyle(el).direction);
    expect(codeDir, 'inline code stays LTR under RTL').toBe('ltr');
  });

  test('10.5-INT-014: non-default locale URL carries locale segment (/zh-CN/quickstart)', async ({ page }) => {
    // P1 | integration | BR-10.5.11
    const resp = await page.goto('/zh-CN/quickstart');
    expect(resp?.status()).toBe(200);
    expect(page.url()).toContain('/zh-CN/quickstart');
  });

  test('10.5-INT-015: completeness gate emits WARNING (non-blocking) for MT-unproofed locales (7 langs → 10.7)', () => {
    // P1 | integration | OQ-10.5-4 severity split (WARN ≠ BLOCK)
    const r = tsx('check-docs-completeness.ts');
    expect(r.code, 'WARN is non-blocking').toBe(0);
    for (const loc of ['ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru']) {
      expect(r.out).toContain(`locale "${loc}" is MT-seeded`);
    }
    expect(r.out).toMatch(/DEFERRED to Story 10\.7/);
  });

  test('10.5-E2E-009: language switch stays on current page (/quickstart → zh-CN → /zh-CN/quickstart)', async ({ page }) => {
    // P1 | e2e | BR-10.5.11 切换不丢上下文
    await page.goto('/quickstart');
    // Docusaurus locale dropdown links to the SAME page in the target locale.
    const zhLink = page.locator('a.dropdown__link[href="/zh-CN/quickstart"]');
    await expect(zhLink).toHaveCount(1);
    await page.locator('.navbar__items--right .dropdown, .navbar .dropdown').first().hover();
    await zhLink.click();
    await expect(page).toHaveURL(/\/zh-CN\/quickstart\/?$/);
  });

  test('10.5-E2E-010: fallback UX — locale missing a page lands on en content (not 404/blank)', async ({ page }) => {
    // P1 | e2e | pairs with INT-013. zh-CN does not translate sdk/python → en fallback served.
    const resp = await page.goto('/zh-CN/sdk/python');
    expect(resp?.status()).toBe(200);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    expect(await page.locator('main').innerText()).toContain('pip install he-api');
  });

  test('10.5-E2E-011: 10-locale smoke loop — home + 3 sections reachable per locale', async ({ page }) => {
    // P2 | e2e | structural-only for the 7 MT-unproofed langs (proof → 10.7)
    for (const loc of LOCALES) {
      const prefix = loc === 'en' ? '' : `/${loc}`;
      const home = await page.goto(`${prefix}/`);
      expect(home?.status(), `${loc} home`).toBe(200);
      for (const section of SECTIONS) {
        const r = await page.goto(`${prefix}/${section}`);
        expect(r?.status(), `${loc}/${section}`).toBe(200);
      }
    }
  });

  test('10.5-INT-016: provenance — en+zh-CN human-authored; 8 langs carry MT-seeded marker', () => {
    // P2 | integration | OQ-10.5-4 authoring split
    expect(readDoc('quickstart.mdx')).toMatch(/he_provenance:\s*human/);
    const i18n = (loc: string, p: string) =>
      readFileSync(join(DOCS_ROOT, 'i18n', loc, 'docusaurus-plugin-content-docs', 'current', p), 'utf8');
    expect(i18n('zh-CN', 'quickstart.mdx')).toMatch(/he_provenance:\s*human/);
    for (const loc of ['ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar']) {
      expect(i18n(loc, 'quickstart.mdx'), `${loc} MT marker`).toMatch(/he_provenance:\s*mt/);
    }
  });

  // --- Blind Spot Scenarios ---

  test('[BLIND-SPOT] 10.5-BLIND-ERROR-002: unsupported locale (/zh-TW/quickstart) → 404, no crash/blank', async ({ page }) => {
    // ERROR | P1 | ERROR-003
    const resp = await page.goto('/zh-TW/quickstart');
    expect(resp?.status()).toBe(404);
    await expect(page.locator('body')).toContainText(/(Page Not Found|404)/i);
  });

  test('[BLIND-SPOT] 10.5-BLIND-BOUNDARY-001: RTL bidi isolation — Arabic prose + inline LTR token', async ({ page }) => {
    // BOUNDARY | P1 | BOUNDARY-005. The .he-ltr island isolates inline LTR tokens in Arabic prose.
    await page.goto('/ar/');
    const island = page.locator('.he-ltr').first();
    await expect(island).toBeVisible(); // ensure stylesheet applied before reading computed style
    const style = await island.evaluate((el) => {
      const s = getComputedStyle(el);
      return { direction: s.direction, bidi: s.unicodeBidi };
    });
    expect(style.direction).toBe('ltr');
    expect(style.bidi).toMatch(/isolate/);
  });

  test('[BLIND-SPOT] 10.5-BLIND-FLOW-003: direct deep-link into RTL locale (/ar/api-reference) renders RTL', async ({ page }) => {
    // FLOW | P1 | FLOW-005
    const resp = await page.goto('/ar/api-reference');
    expect(resp?.status()).toBe(200);
    expect(await page.locator('html').getAttribute('dir')).toBe('rtl');
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  });

  test('[BLIND-SPOT] 10.5-BLIND-FLOW-002: switch language then browser-back retains prior locale page', async ({ page }) => {
    // FLOW | P2 | FLOW-005
    await page.goto('/quickstart');
    await page.goto('/zh-CN/quickstart');
    await page.goBack();
    await expect(page).toHaveURL(/\/quickstart\/?$/);
    expect(page.url()).not.toContain('/zh-CN/');
  });
});
