#!/usr/bin/env tsx
/**
 * check-i18n-keys.ts — Locale completeness + ICU plural enforcement (Story 2.1, BR-4.6).
 *
 * Validates:
 *   1. every non-en locale's flattened key set equals en's key set (key-by-key)
 *   2. every key value referencing ICU `plural` has at least the required CLDR branches
 *      for that locale (en: `=0`, `=1`, `other`; ar: `zero` `one` `two` `few` `many` `other`;
 *      zh-CN: at least `other`; others: at least `other`)
 *
 * Exit codes: 0 = all locales valid; 1 = missing keys / plural-branch mismatch / JSON error.
 *
 * Optional argv:
 *   --root <path>  Override repo root (default: parent of this script's directory)
 */
import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs';
import { resolve, join, basename, extname } from 'node:path';

interface PluralRule {
  required: ReadonlyArray<string>;
}

const PLURAL_RULES: Record<string, PluralRule> = {
  en: { required: ['=0', '=1', 'other'] },
  'zh-CN': { required: ['other'] },
  ja: { required: ['other'] },
  ko: { required: ['other'] },
  es: { required: ['one', 'other'] },
  fr: { required: ['one', 'other'] },
  de: { required: ['one', 'other'] },
  pt: { required: ['one', 'other'] },
  ru: { required: ['one', 'few', 'many', 'other'] },
  ar: { required: ['zero', 'one', 'two', 'few', 'many', 'other'] },
};

type JsonObject = { [k: string]: unknown };

function isObject(v: unknown): v is JsonObject {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function flatten(obj: JsonObject, prefix = ''): Map<string, unknown> {
  const out = new Map<string, unknown>();
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k;
    if (isObject(v)) {
      for (const [kk, vv] of flatten(v, path)) out.set(kk, vv);
    } else {
      out.set(path, v);
    }
  }
  return out;
}

function pluralBranches(message: string): string[] | null {
  // very lightweight ICU-plural extractor (best-effort)
  const start = message.indexOf('plural');
  if (start < 0) return null;
  const after = message.slice(start);
  // find branches like `=0 {...}`, `one {...}`, `other {...}` (no nested-arg parsing)
  const re = /(=\d+|zero|one|two|few|many|other)\s*\{/g;
  const branches: string[] = [];
  let m: RegExpExecArray | null;
  while ((m = re.exec(after)) !== null) {
    if (m[1] !== undefined) branches.push(m[1]);
  }
  return branches.length ? branches : null;
}

function parseArgs(): { root: string } {
  const args = process.argv.slice(2);
  let root = resolve(__dirname, '..');
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--root' && args[i + 1]) {
      const next = args[i + 1];
      if (next) {
        root = resolve(next);
        i++;
      }
    }
  }
  return { root };
}

function discoverNamespaces(enDir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(enDir)) {
    const full = join(enDir, entry);
    if (!statSync(full).isFile()) continue;
    if (extname(entry) !== '.json') continue;
    out.push(basename(entry, '.json'));
  }
  return out.sort();
}

function loadLocale(messagesRoot: string, locale: string, namespace: string): Map<string, unknown> {
  const p = join(messagesRoot, locale, `${namespace}.json`);
  if (!existsSync(p)) {
    console.error(`❌ ${locale}/${namespace}.json: missing file`);
    process.exit(1);
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(readFileSync(p, 'utf8'));
  } catch (err) {
    console.error(`❌ ${locale}/${namespace}.json: JSON parse error — ${(err as Error).message}`);
    process.exit(1);
  }
  if (!isObject(parsed)) {
    console.error(`❌ ${locale}/${namespace}.json: top-level must be an object`);
    process.exit(1);
  }
  return flatten(parsed);
}

function main(): void {
  const { root } = parseArgs();
  const messagesRoot = join(root, 'apps/console/messages');
  const enDir = join(messagesRoot, 'en');
  if (!existsSync(enDir)) {
    console.error(`❌ ${enDir} not found — cannot run completeness check`);
    process.exit(1);
  }

  const namespaces = discoverNamespaces(enDir);
  if (namespaces.length === 0) {
    console.error(`❌ No namespaces under ${enDir}`);
    process.exit(1);
  }

  const locales = readdirSync(messagesRoot).filter((d) => {
    try {
      return statSync(join(messagesRoot, d)).isDirectory();
    } catch {
      return false;
    }
  });

  let failed = false;

  for (const ns of namespaces) {
    const enFlat = loadLocale(messagesRoot, 'en', ns);
    const enKeys = new Set(enFlat.keys());

    for (const locale of locales) {
      if (locale === 'en') continue;
      const flat = loadLocale(messagesRoot, locale, ns);
      const keys = new Set(flat.keys());

      const missing: string[] = [];
      const extra: string[] = [];
      for (const k of enKeys) if (!keys.has(k)) missing.push(k);
      for (const k of keys) if (!enKeys.has(k)) extra.push(k);

      if (missing.length > 0) {
        console.error(`❌ Key missing in ${locale}/${ns}.json: ${missing.join(', ')}`);
        failed = true;
      }
      if (extra.length > 0) {
        console.error(`❌ Extra keys in ${locale}/${ns}.json (not in en): ${extra.join(', ')}`);
        failed = true;
      }

      // plural branch check for shared keys
      const rule = PLURAL_RULES[locale];
      if (rule) {
        for (const k of enKeys) {
          const enVal = enFlat.get(k);
          if (typeof enVal !== 'string') continue;
          const enBranches = pluralBranches(enVal);
          if (!enBranches) continue;
          const localeVal = flat.get(k);
          if (typeof localeVal !== 'string') continue;
          const localeBranches = pluralBranches(localeVal);
          if (!localeBranches) {
            console.error(`❌ Plural branches mismatch in ${locale}/${ns}.json @ ${k}: not a plural pattern`);
            failed = true;
            continue;
          }
          const missingBranches = rule.required.filter((b) => !localeBranches.includes(b));
          if (missingBranches.length > 0) {
            console.error(
              `❌ Plural branches mismatch in ${locale}/${ns}.json @ ${k}: missing ${missingBranches.join(', ')}`
            );
            failed = true;
          }
        }
      }
    }
  }

  if (failed) {
    console.error(`\n❌ i18n key completeness check FAILED`);
    process.exit(1);
  }
  console.log(`✅ i18n key completeness check passed (${namespaces.length} namespace(s) × ${locales.length} locale(s))`);
}

main();
