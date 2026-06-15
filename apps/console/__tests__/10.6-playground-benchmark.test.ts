/**
 * Story 10.6 — Console-unit (C-U) tests for Playground + Benchmark.
 *
 * Implemented from the QA Test Design skeleton (Turing, 2026-06-16). Each test
 * maps to a designed scenario in docs/qa/assessments/10.6-test-design-20260616.md.
 *
 * Gateway (GW) scenarios live in apps/api-gateway/internal/handlers/playground_chat_test.go;
 * E2E (C-E) scenarios in apps/console/e2e/{playground,marketing-benchmark}.spec.ts.
 */
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, test, expect } from 'vitest';

import { generateExportSnippet } from '@/lib/playground/export-snippets';
import { estimatePlaygroundCost } from '@/lib/playground/cost';
import { parseDeepLinkFragment, buildDeepLinkFragment } from '@/lib/playground/deep-link';
import { buildAbModels } from '@/lib/playground/ab';
import { canSend, isValidTemperature, isValidMaxTokens } from '@/lib/playground/validation';
import { pricingFor } from '@/lib/catalogue/pricing';
import { loadBenchmarkData, assembleBenchmark, BenchmarkSeedSchema } from '@/lib/api/benchmark';
import { BENCHMARK_SEED } from '@/lib/benchmark/seed';

const LOCALES = ['en', 'zh-CN', 'ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar'];

function flattenKeys(obj: Record<string, unknown>, prefix = '', out: string[] = []): string[] {
  for (const k of Object.keys(obj)) {
    const v = obj[k];
    const key = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === 'object' && !Array.isArray(v)) flattenKeys(v as Record<string, unknown>, key, out);
    else out.push(key);
  }
  return out;
}

function loadNamespace(locale: string, ns: string): Record<string, unknown> {
  return JSON.parse(readFileSync(join(process.cwd(), 'messages', locale, `${ns}.json`), 'utf8'));
}

const params = { model: 'qwen-max', system: 'You are helpful.', user: 'Tell me a fun fact about pandas', temperature: 0.7, maxTokens: 256 };

// ============================================================
// AC1: Playground — Export snippet generators (FR-11.2)
// ============================================================

describe('AC1: Playground — Export snippet generators (FR-11.2)', () => {
  test('10.6-UNIT-001: Export cURL from current model+params+messages', () => {
    const snip = generateExportSnippet('curl', params);
    expect(snip).toContain('/v1/chat/completions');
    expect(snip).toContain('Authorization: Bearer');
    expect(snip).toContain('"qwen-max"');
    expect(snip).toContain('Tell me a fun fact about pandas');
  });

  test('10.6-UNIT-002: Export Python parity with 10.2 SDK', () => {
    const snip = generateExportSnippet('python', params);
    expect(snip).toContain('from he_api import Client');
    expect(snip).toContain('client.chat.completions.create(');
  });

  test('10.6-UNIT-003: Export TypeScript parity with 10.3 SDK', () => {
    const snip = generateExportSnippet('typescript', params);
    expect(snip).toContain('import { Client } from "@he-api/sdk"');
    expect(snip).toContain('client.chat.completions.create(');
  });

  test('10.6-UNIT-004: Export Go parity with 10.4 SDK', () => {
    const snip = generateExportSnippet('go', params);
    expect(snip).toContain('heapi.NewClient()');
    expect(snip).toContain('client.Chat.Completions.New(ctx,');
  });
});

describe('AC1: Playground — cost estimate (money-SoT guard, P0)', () => {
  test('10.6-UNIT-005: cost = catalogue pricing × usage, labeled "estimated", NEVER reads X-He-Cost-Usd/usage_ledger', () => {
    const p = pricingFor('qwen-max')!;
    const usage = { prompt_tokens: 1000, completion_tokens: 500, total_tokens: 1500 };
    const est = estimatePlaygroundCost('qwen-max', usage);
    expect(est).not.toBeNull();
    // Derived purely from catalogue pricing × usage (deterministic, no external read).
    const expected = (1000 * p.inputPerMTokens + 500 * p.outputPerMTokens) / 1_000_000;
    expect(est!.amountUsd).toBeCloseTo(expected, 9);
    expect(est!.estimated).toBe(true);
    expect(est!.source).toBe('catalogue');
    // Unknown model → no fabricated/billed figure.
    expect(estimatePlaygroundCost('gpt-4', usage)).toBeNull();
  });
});

describe('AC1: Playground — deep-link #fragment validation (injection security, P0)', () => {
  test('10.6-UNIT-006: valid #fragment → model∈catalogue + param type-check; code inserted as PLAIN TEXT, never eval', () => {
    // a payload whose "user" code is a JS-looking string must be returned VERBATIM, not executed.
    const frag = buildDeepLinkFragment({ model: 'qwen-max', user: 'process.exit(1); // not executed', temperature: 0.5, maxTokens: 128 });
    const pre = parseDeepLinkFragment(frag);
    expect(pre).not.toBeNull();
    expect(pre!.model).toBe('qwen-max');
    expect(pre!.user).toBe('process.exit(1); // not executed'); // literal string, no execution
    expect(pre!.temperature).toBe(0.5);
    expect(pre!.maxTokens).toBe(128);
  });

  test('10.6-UNIT-007: malicious/invalid #fragment payload → ignored gracefully, no crash, no eval', () => {
    // unknown model → whole payload ignored
    expect(parseDeepLinkFragment(buildDeepLinkFragment({ model: 'evil-model', user: 'hi' } as never))).toBeNull();
    // wrong-typed temperature (string) → param dropped, model still applied
    const bad = '#prefill=' + encodeURIComponent(JSON.stringify({ model: 'qwen-max', user: 'hi', temperature: 'DROP TABLE' }));
    const pre = parseDeepLinkFragment(bad);
    expect(pre).not.toBeNull();
    expect(pre!.temperature).toBeUndefined();
    // garbage / non-JSON fragment → null, no throw
    expect(parseDeepLinkFragment('#prefill=%%%not-json%%%')).toBeNull();
    expect(parseDeepLinkFragment('#prefill=' + encodeURIComponent('not json'))).toBeNull();
  });
});

describe('AC1: Playground — A/B header assembly (front-end pre-validation)', () => {
  test('10.6-UNIT-008: X-He-AB-Models built with exactly 2 distinct ids; rejects duplicate / he-router-*', () => {
    const ok = buildAbModels(['qwen-max', 'deepseek-v3']);
    expect(ok.ok).toBe(true);
    if (ok.ok) expect(ok.header).toBe('qwen-max,deepseek-v3');
    expect(buildAbModels(['qwen-max']).ok).toBe(false); // count != 2
    expect(buildAbModels(['qwen-max', 'qwen-max']).ok).toBe(false); // duplicate
    expect(buildAbModels(['qwen-max', 'he-router-cost']).ok).toBe(false); // router rejected
  });
});

describe('AC1: Playground — i18n', () => {
  test('10.6-UNIT-009: playground.json 10 locales present; missing key → fallback en', () => {
    const enKeys = new Set(flattenKeys(loadNamespace('en', 'playground')));
    expect(enKeys.size).toBeGreaterThan(0);
    for (const loc of LOCALES) {
      const keys = new Set(flattenKeys(loadNamespace(loc, 'playground')));
      expect([...enKeys].filter((k) => !keys.has(k))).toEqual([]); // every en key present (no drift → en fallback works)
    }
  });
});

// --- AC1 Blind Spot Scenarios (console-unit) ---

describe('AC1: Playground — blind spots [BLIND-SPOT]', () => {
  test('[BLIND-SPOT] 10.6-BLIND-BOUNDARY-001: empty User message → send disabled', () => {
    expect(canSend('')).toBe(false);
    expect(canSend('   ')).toBe(false);
    expect(canSend('hi')).toBe(true);
  });

  test('[BLIND-SPOT] 10.6-BLIND-BOUNDARY-002: temperature ∉ [0,2] / max_tokens ≤ 0 → front-end constraint', () => {
    expect(isValidTemperature(0)).toBe(true);
    expect(isValidTemperature(2)).toBe(true);
    expect(isValidTemperature(-0.1)).toBe(false);
    expect(isValidTemperature(2.1)).toBe(false);
    expect(isValidMaxTokens(1)).toBe(true);
    expect(isValidMaxTokens(0)).toBe(false);
    expect(isValidMaxTokens(-5)).toBe(false);
    expect(isValidMaxTokens(1.5)).toBe(false);
  });

  test('[BLIND-SPOT] 10.6-BLIND-BOUNDARY-004: deep-link fragment empty / oversized / non-UTF8 → ignored gracefully', () => {
    expect(parseDeepLinkFragment('')).toBeNull();
    expect(parseDeepLinkFragment('#')).toBeNull();
    expect(parseDeepLinkFragment(null)).toBeNull();
    expect(parseDeepLinkFragment('#prefill=' + 'x'.repeat(9000))).toBeNull(); // oversized
    expect(parseDeepLinkFragment('#prefill=%E0%A4%A')).toBeNull(); // malformed percent-encoding (non-UTF8)
  });

  test('[BLIND-SPOT] 10.6-BLIND-DATA-002: cost estimate never claimed as billed amount; usage_ledger remains sole money SoT', () => {
    const est = estimatePlaygroundCost('qwen-max', { prompt_tokens: 10, completion_tokens: 10 });
    expect(est).not.toBeNull();
    expect(est!.estimated).toBe(true); // the type forbids any "billed" flag — only `estimated: true`
    expect(Object.keys(est!)).not.toContain('billed');
  });
});

// ============================================================
// AC2: 公开 Benchmark 跑分页 — console-unit logic
// ============================================================

describe('AC2: Benchmark — data source & SoT', () => {
  test('10.6-UNIT-010: Zod safeParse — valid seed parses; shape drift → graceful degrade (no crash/500)', () => {
    const ok = loadBenchmarkData();
    expect(ok.ok).toBe(true);
    if (ok.ok) expect(ok.data.rows.length).toBeGreaterThan(0);
    // shape drift (points not an array) → graceful { ok:false }, never throws
    const drift = loadBenchmarkData({ last_updated: '2026-01-01', methodologySources: ['x'], points: 'nope' });
    expect(drift.ok).toBe(false);
  });

  test('10.6-UNIT-011: He 6-vendor pricing rendered FROM models-catalogue SoT (not hardcoded); non-He from curated seed', () => {
    const ok = loadBenchmarkData();
    expect(ok.ok).toBe(true);
    if (!ok.ok) return;
    const qwen = ok.data.rows.find((r) => r.modelId === 'qwen-max')!;
    const p = pricingFor('qwen-max')!;
    const expected = Math.round(((p.inputPerMTokens + p.outputPerMTokens) / 2) * 100) / 100;
    expect(qwen.costPer1M).toBe(expected); // from catalogue, NOT the seed
    // non-He reference model price comes from the curated seed
    const gpt4 = ok.data.rows.find((r) => r.modelId === 'gpt-4')!;
    expect(gpt4.costPer1M).toBe(30.0);
    // the He seed point itself carries NO price (proves not hardcoded)
    const heSeed = BENCHMARK_SEED.points.find((pt) => pt.modelId === 'qwen-max')!;
    expect((heSeed as Record<string, unknown>).costPer1M).toBeUndefined();
  });

  test('10.6-UNIT-012: provenance present — per-point source + last_updated + methodology + disclaimer', () => {
    const ok = loadBenchmarkData();
    expect(ok.ok).toBe(true);
    if (!ok.ok) return;
    expect(ok.data.lastUpdated).toBeTruthy();
    expect(ok.data.methodologySources.length).toBeGreaterThan(0);
    for (const row of ok.data.rows) expect(row.source.length).toBeGreaterThan(0);
    // visible data disclaimer lives in the benchmark i18n namespace
    const en = loadNamespace('en', 'benchmark') as { disclaimer?: string };
    expect(typeof en.disclaimer).toBe('string');
    expect(en.disclaimer!.length).toBeGreaterThan(0);
  });

  test('10.6-UNIT-013: benchmark.json 10 locales + fallback en', () => {
    const enKeys = new Set(flattenKeys(loadNamespace('en', 'benchmark')));
    for (const loc of LOCALES) {
      const keys = new Set(flattenKeys(loadNamespace(loc, 'benchmark')));
      expect([...enKeys].filter((k) => !keys.has(k))).toEqual([]);
    }
  });
});

// --- AC2 Blind Spot Scenarios (console-unit) ---

describe('AC2: Benchmark — blind spots [BLIND-SPOT]', () => {
  test('[BLIND-SPOT] 10.6-BLIND-BOUNDARY-005: seed missing model / missing metric → graceful degrade', () => {
    // missing required metric (quality) on a point → safeParse fails → { ok:false }
    const bad = { last_updated: '2026-01-01', methodologySources: ['x'], points: [{ modelId: 'qwen-max', vendorSlug: 'alibaba', vendor: 'Alibaba', isHe: true, latencyP95Ms: 900, source: 's' }] };
    expect(loadBenchmarkData(bad).ok).toBe(false);
    // a He model absent from the catalogue → its row is dropped, not rendered as 0
    const unknownHe = BenchmarkSeedSchema.parse({ last_updated: '2026-01-01', methodologySources: ['x'], points: [{ modelId: 'not-in-catalogue', vendorSlug: 'x', vendor: 'X', isHe: true, quality: 80, latencyP95Ms: 900, source: 's' }] });
    expect(assembleBenchmark(unknownHe).rows.length).toBe(0);
  });

  test('[BLIND-SPOT] 10.6-BLIND-DATA-003: He pricing always tracks catalogue (catalogue change reflects; seed never overrides)', () => {
    // even if a He seed point smuggled a costPer1M, assemble uses the catalogue value.
    const seed = BenchmarkSeedSchema.parse({
      last_updated: '2026-01-01',
      methodologySources: ['x'],
      points: [{ modelId: 'deepseek-v3', vendorSlug: 'deepseek', vendor: 'DeepSeek', isHe: true, quality: 88, latencyP95Ms: 760, costPer1M: 999.99, source: 's' }],
    });
    const row = assembleBenchmark(seed).rows.find((r) => r.modelId === 'deepseek-v3')!;
    const p = pricingFor('deepseek-v3')!;
    expect(row.costPer1M).toBe(Math.round(((p.inputPerMTokens + p.outputPerMTokens) / 2) * 100) / 100);
    expect(row.costPer1M).not.toBe(999.99); // seed never overrides the catalogue SoT
  });
});
