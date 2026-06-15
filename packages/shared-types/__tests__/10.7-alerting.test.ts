/**
 * Story 10.7 AC1 — Alertmanager wiring + gateway RED rules (static assertions).
 *
 * Designed against the Architect-ratified OQ-10.7-1 / OQ-10.7-5 contracts + the
 * QA test design. The Alertmanager config (plain YAML in the kube-prometheus-
 * stack staging values) is parsed with `yaml`; the gateway PrometheusRule is a
 * Helm template, so it is asserted via raw-text/regex (the bindings convention).
 *
 *   L-001 critical → pagerduty + 飞书 mirror (continue:true)   L-002 *_file refs
 *   UNIT-002 non-blackhole catch-all   UNIT-003 secret-scan   UNIT-004 no-PII tmpl
 *   L-003/UNIT-005 gateway RED rules (DataExportSuspect sentinel)   L-006 matrix
 *   L-007 full label set
 *
 * Toolchain note ([[project_toolchain_env_limits]]): no helm/yq locally — Helm
 * render + the route gold-gate run in CI (helm-lint-observability matrix +
 * verify-alertmanager-routes.sh). These parse-level assertions catch structural
 * regressions on PR.
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

const VALUES = 'infra/helm/observability/kube-prometheus-stack/values-staging.yaml';
const GATEWAY_RULE = 'infra/helm/api-gateway/templates/prometheusrule.yaml';
const EXTERNAL_SECRET = 'infra/helm/observability/kube-prometheus-stack/templates/externalsecret-alerting.yaml';
const GOLD_GATE = 'scripts/ci/verify-alertmanager-routes.sh';

interface Route {
  receiver?: string;
  matchers?: string[];
  continue?: boolean;
  routes?: Route[];
}
interface Receiver {
  name: string;
  webhook_configs?: unknown[];
  pagerduty_configs?: Array<Record<string, unknown>>;
  slack_configs?: Array<Record<string, unknown>>;
  email_configs?: unknown[];
}
interface AmConfig {
  route: Route;
  receivers: Receiver[];
}

function loadAm(): AmConfig {
  const v = parseYaml(read(VALUES)) as { alertmanager?: { config?: AmConfig } };
  const cfg = v.alertmanager?.config;
  if (!cfg) throw new Error('alertmanager.config missing from staging values');
  return cfg;
}

function* walk(obj: unknown, path: string[] = []): Generator<[string[], unknown]> {
  if (obj === null || typeof obj !== 'object') {
    yield [path, obj];
    return;
  }
  if (Array.isArray(obj)) {
    for (let i = 0; i < obj.length; i++) yield* walk(obj[i], [...path, String(i)]);
    return;
  }
  for (const [k, v] of Object.entries(obj)) yield* walk(v, [...path, k]);
}

describe('10.7 AC1 — files exist', () => {
  test('the gateway PrometheusRule, alerting ExternalSecret, and gold gate exist', () => {
    expect(existsSync(p(GATEWAY_RULE))).toBe(true);
    expect(existsSync(p(EXTERNAL_SECRET))).toBe(true);
    expect(existsSync(p(GOLD_GATE))).toBe(true);
  });
});

describe('10.7-L-001 / DATA-001 — critical → pagerduty + 飞书 dual-channel', () => {
  const cfg = loadAm();
  const criticalRoutes = (cfg.route.routes ?? []).filter((r) =>
    (r.matchers ?? []).some((m) => /severity\s*=\s*"critical"/.test(m)),
  );

  test('a critical route targets pagerduty and sets continue:true', () => {
    const pd = criticalRoutes.find((r) => r.receiver === 'pagerduty');
    expect(pd).toBeTruthy();
    expect(pd!.continue).toBe(true);
  });

  test('a critical route also mirrors to 飞书 (no single-channel silence)', () => {
    expect(criticalRoutes.some((r) => r.receiver === 'feishu')).toBe(true);
  });

  test('every critical route resolves to a defined receiver (no dangling)', () => {
    const names = new Set(cfg.receivers.map((r) => r.name));
    for (const r of criticalRoutes) expect(names.has(r.receiver!)).toBe(true);
  });
});

describe('10.7-UNIT-002 — explicit non-blackhole catch-all', () => {
  const cfg = loadAm();
  test('the route default receiver is catch-all and it notifies (not a blackhole)', () => {
    expect(cfg.route.receiver).toBe('catch-all');
    const catchAll = cfg.receivers.find((r) => r.name === 'catch-all');
    expect(catchAll).toBeTruthy();
    const notifiers =
      (catchAll!.webhook_configs?.length ?? 0) +
      (catchAll!.pagerduty_configs?.length ?? 0) +
      (catchAll!.slack_configs?.length ?? 0) +
      (catchAll!.email_configs?.length ?? 0);
    expect(notifiers).toBeGreaterThan(0);
  });
});

describe('10.7-UNIT-003 / L-002 — receiver secrets via *_file only', () => {
  const cfg = loadAm();
  test('no inline routing_key / api_url / smtp password; only *_file refs', () => {
    for (const [path, value] of walk(cfg)) {
      const key = path[path.length - 1] ?? '';
      // The secret-bearing keys must be the *_file variants.
      expect(key).not.toBe('routing_key');
      expect(key).not.toBe('api_url');
      expect(key).not.toBe('smtp_auth_password');
      // No primitive may contain a plaintext webhook / integration secret.
      if (typeof value === 'string') {
        expect(value).not.toMatch(/hooks\.slack\.com|open\.feishu\.cn\/open-apis|events\.pagerduty\.com\/v2/);
      }
    }
  });

  test('every *_file ref points under the mounted secret path', () => {
    let fileRefs = 0;
    for (const [path, value] of walk(cfg)) {
      const key = path[path.length - 1] ?? '';
      if (/_file$/.test(key) && typeof value === 'string') {
        fileRefs += 1;
        expect(value).toMatch(/^\/etc\/alertmanager\/secrets\/alertmanager-alerting-secrets\//);
      }
    }
    expect(fileRefs).toBeGreaterThanOrEqual(2); // pagerduty + feishu at minimum
  });
});

describe('10.7-UNIT-004 — PagerDuty payload carries no PII', () => {
  const cfg = loadAm();
  const ALLOWED = /^(\{\{[^}]*\}\}|.*\.(alertname|severity|service|team|summary|description|runbook_url).*)$/;
  test('pagerduty details/description interpolate only allow-listed metadata', () => {
    const pd = cfg.receivers.find((r) => r.name === 'pagerduty')!.pagerduty_configs![0];
    const fields: string[] = [];
    if (typeof pd.description === 'string') fields.push(pd.description);
    if (pd.details && typeof pd.details === 'object') {
      fields.push(...Object.values(pd.details as Record<string, string>));
    }
    expect(fields.length).toBeGreaterThan(0);
    for (const f of fields) {
      // never interpolate identity / content
      expect(f).not.toMatch(/user_id|\.Email|email|client_ip|\bip\b|prompt|request_body|message/i);
      // every templated field references an allow-listed label/annotation
      if (f.includes('{{')) expect(f).toMatch(ALLOWED);
    }
  });
});

describe('10.7-L-006 — §11.4 channel matrix', () => {
  const cfg = loadAm();
  const byChannel = (val: string) =>
    (cfg.route.routes ?? []).find((r) => (r.matchers ?? []).some((m) => new RegExp(`channel\\s*=\\s*"${val}"`).test(m)));
  test('channel=feishu → 飞书+email receiver; channel=slack → 飞书+slack receiver', () => {
    const feishu = byChannel('feishu');
    expect(feishu?.receiver).toBe('feishu-email');
    const fe = cfg.receivers.find((r) => r.name === 'feishu-email')!;
    expect((fe.webhook_configs?.length ?? 0) > 0 && (fe.email_configs?.length ?? 0) > 0).toBe(true);

    const slack = byChannel('slack');
    expect(slack?.receiver).toBe('feishu-slack');
    const fs = cfg.receivers.find((r) => r.name === 'feishu-slack')!;
    expect((fs.webhook_configs?.length ?? 0) > 0 && (fs.slack_configs?.length ?? 0) > 0).toBe(true);
  });
});

describe('10.7-L-003 / L-007 / UNIT-005 — gateway §11.3 RED rules', () => {
  const src = read(GATEWAY_RULE);
  const rules = ['GatewayP95LatencyHigh', 'GatewayErrorRateHigh', 'UpstreamModelDown', 'DataExportSuspect'];

  test('all four §11.3 rules are present', () => {
    for (const r of rules) expect(src).toContain(`alert: ${r}`);
  });

  test('GatewayErrorRateHigh + DataExportSuspect are critical · channel=pagerduty', () => {
    // each critical alert's block carries severity: critical and channel: pagerduty
    for (const r of ['GatewayErrorRateHigh', 'DataExportSuspect']) {
      const block = src.slice(src.indexOf(`alert: ${r}`), src.indexOf(`alert: ${r}`) + 600);
      expect(block).toMatch(/severity:\s*critical/);
      expect(block).toMatch(/channel:\s*pagerduty/);
    }
  });

  test('UNIT-005 — DataExportSuspect fires on overseas egress (§9.1 sentinel)', () => {
    const block = src.slice(src.indexOf('alert: DataExportSuspect'));
    expect(block).toContain('network_egress_to_overseas_bytes');
  });

  test('L-007 — every rule carries severity+channel+team+service+runbook_url', () => {
    expect((src.match(/severity:/g) ?? []).length).toBeGreaterThanOrEqual(4);
    expect((src.match(/channel:/g) ?? []).length).toBeGreaterThanOrEqual(4);
    expect((src.match(/team:\s*gateway/g) ?? []).length).toBeGreaterThanOrEqual(4);
    expect((src.match(/service:\s*api-gateway/g) ?? []).length).toBeGreaterThanOrEqual(4);
    expect((src.match(/runbook_url:/g) ?? []).length).toBeGreaterThanOrEqual(4);
  });
});
