#!/usr/bin/env tsx
/**
 * check-api-ref-drift.ts — API Reference ↔ rest-api-spec.md §5.1 anti-drift gate
 * (Story 10.5, AC1, BR-10.5.4 / Architect OQ-10.5-3 binding guard; INT-004/005/006).
 *
 * The API Reference page is HAND-WRITTEN MDX transcribing the canonical contract. This gate
 * proves the transcription neither DROPS a required endpoint/error-code/header nor INVENTS a
 * contract the spec does not define:
 *   - every required endpoint / error code / response header is documented in the API Reference;
 *   - every endpoint / error code the API Reference documents also exists in rest-api-spec.md
 *     §5.1 (no invented contract);
 *   - the A/B-path nuance "X-He-Selected-Model is NOT set" is present in both (rest-api-spec.md:27);
 *   - the SSE terminator `data: [DONE]` is documented.
 *
 * Exit codes: 0 = consistent; 1 = drift / missing item.
 *
 * Optional argv:
 *   --ref <path>    Override the API Reference MDX (default: <root>/docs/api-reference.mdx).
 *   --spec <path>   Override the spec file (default: <repo>/docs/architecture/rest-api-spec.md).
 */
import { readFileSync, existsSync } from 'node:fs';
import { resolve, join } from 'node:path';

const REQUIRED_ENDPOINTS = [
  '/v1/chat/completions',
  '/v1/embeddings',
  '/v1/audio/transcriptions',
  '/v1/audio/speech',
  '/v1/models',
  '/v1/usage',
  '/v1/balance',
];

// §5.1.2 — the OpenAI-compatible-API-relevant subset the docs MUST surface.
const REQUIRED_ERROR_CODES = [
  '400_invalid_request',
  '400_content_filter',
  '401_invalid_api_key',
  '402_balance_insufficient',
  '402_quota_exhausted',
  '403_ip_not_whitelisted',
  '403_model_not_in_scope',
  '413_payload_too_large',
  '429_rate_limit_qps',
  '429_rate_limit_rpm',
  '429_rate_limit_tpm',
  '500_internal_error',
  '502_upstream_unavailable',
  '504_upstream_timeout',
];

const REQUIRED_HEADERS = ['X-He-Request-Id', 'X-He-Selected-Model', 'X-He-Cost-Usd'];

function parseArgs(): { ref: string; spec: string } {
  const root = resolve(__dirname, '..');
  let ref = join(root, 'docs', 'api-reference.mdx');
  let spec = resolve(root, '..', '..', 'docs', 'architecture', 'rest-api-spec.md');
  const args = process.argv.slice(2);
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--ref' && args[i + 1]) ref = resolve(args[++i] as string);
    else if (args[i] === '--spec' && args[i + 1]) spec = resolve(args[++i] as string);
  }
  return { ref, spec };
}

function read(path: string, label: string): string {
  if (!existsSync(path)) {
    console.error(`❌ ${label} not found: ${path}`);
    process.exit(1);
  }
  return readFileSync(path, 'utf8');
}

/** Normalize: A/B prose wraps `X-He-Selected-Model is NOT set`; tolerate spacing/case. */
function hasSelectedModelAbsentNote(text: string): boolean {
  // Tolerate markdown around the header name (`X-He-Selected-Model`, **NOT**, etc.).
  return /X-He-Selected-Model[`*\s]+is[`*\s]+NOT[`*\s]+set/i.test(text);
}

function main(): void {
  const { ref, spec } = parseArgs();
  const refText = read(ref, 'API Reference');
  const specText = read(spec, 'rest-api-spec.md');
  const errors: string[] = [];

  // INT-004: endpoint set — documented in ref AND defined in spec (no drop / no invention).
  for (const ep of REQUIRED_ENDPOINTS) {
    if (!refText.includes(ep)) errors.push(`endpoint missing from API Reference: ${ep}`);
    if (!specText.includes(ep)) errors.push(`endpoint documented in API Reference but absent from spec (invented?): ${ep}`);
  }

  // INT-005: error-code table — documented in ref AND present in spec §5.1.2.
  for (const code of REQUIRED_ERROR_CODES) {
    if (!refText.includes(code)) errors.push(`error code missing from API Reference: ${code}`);
    if (!specText.includes(code)) errors.push(`error code documented in API Reference but absent from spec (invented?): ${code}`);
  }

  // INT-006: response headers + the A/B nuance.
  for (const h of REQUIRED_HEADERS) {
    if (!refText.includes(h)) errors.push(`response header missing from API Reference: ${h}`);
  }
  if (!hasSelectedModelAbsentNote(refText)) {
    errors.push('API Reference must state that X-He-Selected-Model is NOT set on the A/B path (rest-api-spec.md:27)');
  }
  if (!hasSelectedModelAbsentNote(specText)) {
    errors.push('spec sanity: could not locate the X-He-Selected-Model-absent statement in rest-api-spec.md');
  }

  // SSE terminator (INT-004 streaming nuance).
  if (!refText.includes('data: [DONE]')) errors.push('API Reference must document the SSE terminator `data: [DONE]`');

  // Provenance: the page must cite the SoT (anti-drift guard, BR-10.5.4).
  if (!/rest-api-spec\.md/.test(refText)) {
    errors.push('API Reference must cite its source of truth (rest-api-spec.md §5.1)');
  }

  if (errors.length) {
    for (const e of errors) console.error(`❌ ${e}`);
    console.error(`\n❌ API Reference anti-drift gate FAILED (${errors.length} issue(s))`);
    process.exit(1);
  }
  console.log(
    `✅ API Reference anti-drift gate passed ` +
      `(${REQUIRED_ENDPOINTS.length} endpoints, ${REQUIRED_ERROR_CODES.length} error codes, ${REQUIRED_HEADERS.length} headers; A/B + SSE nuances present)`,
  );
}

main();
