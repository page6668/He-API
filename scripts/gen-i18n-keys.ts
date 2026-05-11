#!/usr/bin/env tsx
/**
 * gen-i18n-keys.ts — Codegen for `packages/i18n-keys/src/*.ts` (Story 2.1, Architect Q5 ruling).
 *
 * Scans `apps/console/messages/en/*.json` (each file is one namespace) and emits a `.ts`
 * file per namespace under `packages/i18n-keys/src/` declaring a string-literal union
 * type named `{PascalNamespace}Keys`. Also (re)writes `packages/i18n-keys/src/index.ts`
 * to re-export every generated namespace.
 *
 * CI gate: `git diff --exit-code packages/i18n-keys/src/` after build.
 *
 * Algorithm:
 *   1. discover `messages/en/*.json` (per-namespace files; alphabetic order)
 *   2. for each file: JSON.parse → flatten to dot-namespaced keys → sort lexicographically
 *   3. validate every flat key matches `^[a-zA-Z][a-zA-Z0-9_-]*(\.[a-zA-Z0-9][a-zA-Z0-9_-]*)*$`
 *   4. emit `src/{namespace}.ts` containing `export type {Name}Keys = 'key.a' | 'key.b' | ...;`
 *      with the literal `// @generated — DO NOT EDIT` header
 *   5. emit `src/index.ts` with one `export * from './<namespace>';` per namespace
 */
import { readFileSync, readdirSync, writeFileSync, statSync } from 'node:fs';
import { resolve, join, basename, extname } from 'node:path';

const ROOT = resolve(__dirname, '..');
const EN_MESSAGES_DIR = join(ROOT, 'apps/console/messages/en');
const OUT_DIR = join(ROOT, 'packages/i18n-keys/src');

const HEADER = `// @generated — DO NOT EDIT — run \`pnpm --filter @he-api/i18n-keys build\``;
const KEY_RE = /^[a-zA-Z][a-zA-Z0-9_-]*(\.[a-zA-Z0-9][a-zA-Z0-9_-]*)*$/;

type JsonObject = { [k: string]: unknown };

function isObject(v: unknown): v is JsonObject {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function flatten(obj: JsonObject, prefix = ''): string[] {
  const out: string[] = [];
  for (const [k, v] of Object.entries(obj)) {
    const path = prefix ? `${prefix}.${k}` : k;
    if (isObject(v)) {
      out.push(...flatten(v, path));
    } else {
      out.push(path);
    }
  }
  return out;
}

function toPascal(ns: string): string {
  return ns
    .split(/[-_]/)
    .filter(Boolean)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join('');
}

function emit(namespace: string, keys: string[]): string {
  const sorted = [...keys].sort();
  const lines = sorted.map((k, i) => {
    const sep = i === 0 ? '=' : '|';
    return `  ${sep} '${k}'`;
  });
  return [
    HEADER,
    `// Source: apps/console/messages/en/${namespace}.json`,
    `export type ${toPascal(namespace)}Keys`,
    ...lines,
    '  ;',
    '',
  ].join('\n');
}

function emitIndex(namespaces: string[]): string {
  const sorted = [...namespaces].sort();
  return [HEADER, ...sorted.map((n) => `export * from './${n}';`), ''].join('\n');
}

function main(): void {
  let entries: string[];
  try {
    entries = readdirSync(EN_MESSAGES_DIR);
  } catch (err) {
    console.error(`[gen-i18n-keys] Cannot read ${EN_MESSAGES_DIR}: ${(err as Error).message}`);
    process.exit(1);
  }

  const namespaces: string[] = [];
  for (const entry of entries) {
    const full = join(EN_MESSAGES_DIR, entry);
    if (!statSync(full).isFile()) continue;
    if (extname(entry) !== '.json') continue;
    const namespace = basename(entry, '.json');
    const raw = readFileSync(full, 'utf8');
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch (err) {
      console.error(`[gen-i18n-keys] JSON parse error in ${entry}: ${(err as Error).message}`);
      process.exit(1);
    }
    if (!isObject(parsed)) {
      console.error(`[gen-i18n-keys] ${entry}: top-level must be an object`);
      process.exit(1);
    }
    const flat = flatten(parsed);
    for (const k of flat) {
      if (!KEY_RE.test(k)) {
        console.error(`[gen-i18n-keys] Invalid key format in ${entry}: ${k}`);
        process.exit(1);
      }
    }
    if (flat.length === 0) {
      console.error(`[gen-i18n-keys] ${entry}: at least one key required`);
      process.exit(1);
    }
    writeFileSync(join(OUT_DIR, `${namespace}.ts`), emit(namespace, flat), 'utf8');
    namespaces.push(namespace);
    console.log(`[gen-i18n-keys] wrote src/${namespace}.ts (${flat.length} keys)`);
  }

  if (namespaces.length === 0) {
    console.error(`[gen-i18n-keys] No namespaces discovered under ${EN_MESSAGES_DIR}`);
    process.exit(1);
  }

  writeFileSync(join(OUT_DIR, 'index.ts'), emitIndex(namespaces), 'utf8');
  console.log(`[gen-i18n-keys] wrote src/index.ts (${namespaces.length} namespace(s))`);
}

main();
