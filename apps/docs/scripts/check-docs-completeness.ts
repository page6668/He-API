#!/usr/bin/env tsx
/**
 * check-docs-completeness.ts — Docs i18n structural-completeness gate (Story 10.5, AC2).
 *
 * Doc-side analogue of scripts/check-i18n-keys.ts: instead of JSON keys it checks that every
 * locale has the three MUST sections + the SDK index either as a translation OR via the
 * built-in en fallback, and reports translation provenance.
 *
 * Severity (Architect OQ-10.5-4):
 *   - BLOCKING (exit 1): a structural gap — a required page missing from the en SOURCE (nothing
 *     to fall back to), the locale set ≠ the canonical 10, or an empty/blank translated file.
 *   - WARNING (exit 0): a locale is MT-seeded but not yet human-proofread (the 7 langs whose
 *     prose proofreading is deferred to Story 10.7). Tracked, not blocking.
 *
 * Exit codes: 0 = structurally complete (warnings allowed); 1 = structural BLOCK.
 *
 * Optional argv:
 *   --root <path>   Override the docs app root (default: parent of this script's dir).
 *   --strict        Also fail (exit 1) on WARNING (used to assert the warn path in tests).
 */
import { readFileSync, existsSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';

// Canonical locale set — same 10 as the console (10.1); BR-10.5.7 (no zh-TW/hi/id here).
const CANONICAL_LOCALES = ['en', 'zh-CN', 'ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar'] as const;

// Human-authored authoritative sources (Architect OQ-10.5-4 authoring split).
const HUMAN_LOCALES = new Set(['en', 'zh-CN']);
// Arabic proofreading is IN-SCOPE for 10.5 (RTL correctness is safety-critical, D-Q5).
const PROOFED_MT_LOCALES = new Set(['ar']);
// The 7 MT langs whose full prose proofreading is DEFERRED to Story 10.7.
const DEFERRED_PROOF_LOCALES = ['ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru'];

// The required structural set: three MUST sections + the SDK install index (BR-10.5.3).
const REQUIRED_DOCS = ['quickstart', 'api-reference', 'cookbook', 'sdk/index'] as const;

const DOCS_PLUGIN_PATH = 'docusaurus-plugin-content-docs/current';

function parseArgs(): { root: string; strict: boolean } {
  const args = process.argv.slice(2);
  let root = resolve(__dirname, '..');
  let strict = false;
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--root' && args[i + 1]) {
      root = resolve(args[i + 1] as string);
      i++;
    } else if (args[i] === '--strict') {
      strict = true;
    }
  }
  return { root, strict };
}

/** Extract the `const LOCALES = [...] as const;` array from docusaurus.config.ts (the SoT). */
function configuredLocales(root: string): string[] {
  const cfg = join(root, 'docusaurus.config.ts');
  if (!existsSync(cfg)) return [];
  const text = readFileSync(cfg, 'utf8');
  const m = text.match(/const\s+LOCALES\s*=\s*\[([^\]]*)\]/);
  if (!m) return [];
  return [...(m[1] as string).matchAll(/'([^']+)'/g)].map((x) => x[1] as string);
}

/** Source (en) doc path under docs/. */
function enDocPath(root: string, doc: string): string {
  return join(root, 'docs', `${doc}.mdx`);
}

/** Translated doc path under i18n/{locale}/.../current. */
function translatedDocPath(root: string, locale: string, doc: string): string {
  return join(root, 'i18n', locale, DOCS_PLUGIN_PATH, `${doc}.mdx`);
}

function frontmatterValue(file: string, key: string): string | null {
  if (!existsSync(file)) return null;
  const text = readFileSync(file, 'utf8');
  const fm = text.match(/^---\n([\s\S]*?)\n---/);
  if (!fm) return null;
  const line = (fm[1] as string).split('\n').find((l) => l.trim().startsWith(`${key}:`));
  if (!line) return null;
  return line.slice(line.indexOf(':') + 1).trim();
}

function isNonEmptyFile(file: string): boolean {
  try {
    return statSync(file).size > 0 && readFileSync(file, 'utf8').trim().length > 0;
  } catch {
    return false;
  }
}

function main(): void {
  const { root, strict } = parseArgs();
  const errors: string[] = [];
  const warnings: string[] = [];

  // 1. Locale-set check (UNIT-003): config must declare exactly the canonical 10.
  const locales = configuredLocales(root);
  const localeSet = new Set(locales);
  const missingLoc = CANONICAL_LOCALES.filter((l) => !localeSet.has(l));
  const extraLoc = locales.filter((l) => !CANONICAL_LOCALES.includes(l as never));
  if (locales.length === 0) {
    errors.push(`could not read LOCALES from docusaurus.config.ts under ${root}`);
  } else if (missingLoc.length || extraLoc.length || locales.length !== CANONICAL_LOCALES.length) {
    errors.push(
      `locale set mismatch: expected exactly ${CANONICAL_LOCALES.length} ` +
        `[${CANONICAL_LOCALES.join(', ')}], got ${locales.length} [${locales.join(', ')}]` +
        (missingLoc.length ? ` — missing: ${missingLoc.join(', ')}` : '') +
        (extraLoc.length ? ` — extra: ${extraLoc.join(', ')}` : ''),
    );
  } else {
    console.log(`[completeness] locales: ${locales.join(', ')} (${locales.length}) ✓`);
  }

  // 2. Structural en-source check (INT-012 BLOCK): every required doc must exist in en source.
  for (const doc of REQUIRED_DOCS) {
    const p = enDocPath(root, doc);
    if (!existsSync(p)) {
      errors.push(`BLOCK: required section "${doc}" missing from en source (docs/${doc}.mdx) — no en fallback possible`);
    } else if (!isNonEmptyFile(p)) {
      errors.push(`BLOCK: en source "${doc}" is empty (docs/${doc}.mdx)`);
    }
  }

  // 3. Per-locale coverage + provenance.
  const provenance: Record<string, { translated: number; fallback: number }> = {};
  for (const locale of CANONICAL_LOCALES) {
    if (locale === 'en') continue;
    provenance[locale] = { translated: 0, fallback: 0 };
    for (const doc of REQUIRED_DOCS) {
      const tp = translatedDocPath(root, locale, doc);
      if (existsSync(tp)) {
        if (!isNonEmptyFile(tp)) {
          errors.push(`BLOCK: translated "${locale}/${doc}" exists but is empty`);
          continue;
        }
        provenance[locale].translated++;
        // Provenance marker (INT-016): MT-seeded files must declare he_provenance.
        const prov = frontmatterValue(tp, 'he_provenance');
        if (!HUMAN_LOCALES.has(locale) && prov !== 'mt' && prov !== 'human') {
          warnings.push(`${locale}/${doc}: missing he_provenance marker (expected mt|human)`);
        }
      } else {
        // No translation → served via en fallback (BR-10.5.8/.11). Structurally OK.
        provenance[locale].fallback++;
      }
    }
  }

  // 4. MT-unproofed WARNING (INT-015): the 7 deferred langs (proofing → Story 10.7).
  for (const locale of DEFERRED_PROOF_LOCALES) {
    warnings.push(`WARNING: locale "${locale}" is MT-seeded, human proofreading DEFERRED to Story 10.7 (non-blocking)`);
  }

  // 5. Report.
  console.log(
    `[completeness] provenance: human=[${[...HUMAN_LOCALES].join(', ')}] ` +
      `proofed-mt=[${[...PROOFED_MT_LOCALES].join(', ')}] mt-deferred=[${DEFERRED_PROOF_LOCALES.join(', ')}]`,
  );
  for (const locale of CANONICAL_LOCALES) {
    if (locale === 'en') continue;
    const p = provenance[locale];
    console.log(`[completeness]   ${locale}: ${p.translated} translated, ${p.fallback} en-fallback (of ${REQUIRED_DOCS.length})`);
  }
  for (const w of warnings) console.warn(`⚠️  ${w}`);

  if (errors.length) {
    for (const e of errors) console.error(`❌ ${e}`);
    console.error(`\n❌ docs completeness gate FAILED (${errors.length} structural error(s))`);
    process.exit(1);
  }
  if (strict && warnings.length) {
    console.error(`\n❌ docs completeness gate FAILED under --strict (${warnings.length} warning(s))`);
    process.exit(1);
  }
  console.log(`\n✅ docs completeness gate passed (${REQUIRED_DOCS.length} sections × ${CANONICAL_LOCALES.length} locales; warnings non-blocking)`);
}

main();
