/**
 * Story 4.7 — Vitest unit tests for `CapabilityMatrix`.
 *
 * Scenarios:
 *
 *   4.7-UNIT-008 — renders 11 rows for 11-entry input (BR-2.6 desktop)
 *   4.7-UNIT-009 — renders ✓ badge for capabilities.chat=true (BR-2.4 + BR-2.7)
 *   4.7-UNIT-011 — renders ✗ badge for capabilities.vision=false
 *   4.7-UNIT-012 — formats numeric capability per locale via Intl.NumberFormat
 *
 * The fixture mirrors BR-1.3 verbatim — keep in sync with
 * apps/api-gateway/internal/handlers/models.go `capabilitiesByModelID`.
 */

import { describe, test, expect } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { NextIntlClientProvider } from "next-intl";

import { CapabilityMatrix } from "@/components/business/CapabilityMatrix";
import type { ModelEntry } from "@/lib/api/public-models";
import enMessages from "@/messages/en/models.json";
import zhMessages from "@/messages/zh-CN/models.json";

const FIXTURE: ModelEntry[] = [
  { id: "qwen-max", object: "model", created: 1700000000, owned_by: "alibaba", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 32768, max_output_tokens: 8192 } },
  { id: "qwen-plus", object: "model", created: 1700000000, owned_by: "alibaba", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 32768, max_output_tokens: 8192 } },
  { id: "deepseek-v3", object: "model", created: 1700000000, owned_by: "deepseek", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 65536, max_output_tokens: 8192 } },
  { id: "moonshot-v1-128k", object: "model", created: 1700000000, owned_by: "moonshot", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 131072, max_output_tokens: 8192 } },
  { id: "glm-4", object: "model", created: 1700000000, owned_by: "zhipu", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 32768, max_output_tokens: 8192 } },
  { id: "doubao-pro", object: "model", created: 1700000000, owned_by: "bytedance", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: false, context_window_tokens: 32768, max_output_tokens: 8192 } },
  { id: "doubao-lite", object: "model", created: 1700000000, owned_by: "bytedance", capabilities: { chat: true, streaming: true, function_calling: false, vision: false, json_mode: false, context_window_tokens: 32768, max_output_tokens: 4096 } },
  { id: "ernie-4.0", object: "model", created: 1700000000, owned_by: "baidu", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 8192, max_output_tokens: 2048 } },
  { id: "he-router-cost", object: "model", created: 1700000000, owned_by: "he-api", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 131072, max_output_tokens: 8192 } },
  { id: "he-router-quality", object: "model", created: 1700000000, owned_by: "he-api", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 131072, max_output_tokens: 8192 } },
  { id: "he-router-latency", object: "model", created: 1700000000, owned_by: "he-api", capabilities: { chat: true, streaming: true, function_calling: true, vision: false, json_mode: true, context_window_tokens: 131072, max_output_tokens: 8192 } },
];

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function renderWithIntl(locale: string, messages: any) {
  return render(
    <NextIntlClientProvider locale={locale} messages={{ models: messages }}>
      <CapabilityMatrix models={FIXTURE} />
    </NextIntlClientProvider>,
  );
}

describe("CapabilityMatrix (AC2 Vitest)", () => {
  test("4.7-UNIT-008: renders all 11 rows for mock 11-entry input", () => {
    renderWithIntl("en", enMessages);
    const table = screen.getByRole("table");
    expect(table).toBeInTheDocument();
    expect(table.className).toMatch(/md:table/);

    const rows = within(table).getAllByRole("row");
    // 1 header row + 11 model rows = 12 total.
    expect(rows).toHaveLength(12);

    // Each row carries its model id as accessible text via the row header.
    for (const m of FIXTURE) {
      expect(screen.getAllByText(m.id).length).toBeGreaterThan(0);
    }
  });

  test("4.7-UNIT-009: renders ✓ badge for capabilities.chat=true", () => {
    renderWithIntl("en", enMessages);
    // All 11 entries have chat=true; the table renders one badge per entry
    // in the chat column AND each card render adds another. Either way,
    // there are no negative chat badges for this fixture — so a `getAllBy`
    // returns ≥ 11 occurrences of the positive testid.
    const positiveBadges = screen.getAllByTestId("capability-badge-yes");
    expect(positiveBadges.length).toBeGreaterThanOrEqual(11);

    // Sibling sr-only label equals the i18n value.yes string.
    const yesLabel = (enMessages as { capability: { value: { yes: string } } }).capability.value.yes;
    const firstYes = positiveBadges[0]!;
    expect(firstYes.textContent).toContain(yesLabel);

    // Color class anchors the BR-2.4 visual style (≥ 4.5:1 contrast).
    // 设计系统迁移:竹绿语义 token text-jade(#2F6B4F)取代 text-green-600。
    expect(firstYes.className).toMatch(/text-jade/);
  });

  test("4.7-UNIT-011: renders ✗ badge for capabilities.vision=false on every entry", () => {
    renderWithIntl("en", enMessages);
    const negativeBadges = screen.getAllByTestId("capability-badge-no");
    // Vision = false on all 11 entries; doubao-lite also has function_calling=false
    // and doubao-pro / doubao-lite have json_mode=false. Lower bound is 11.
    expect(negativeBadges.length).toBeGreaterThanOrEqual(11);

    const noLabel = (enMessages as { capability: { value: { no: string } } }).capability.value.no;
    const firstNo = negativeBadges[0]!;
    expect(firstNo.textContent).toContain(noLabel);

    // 设计系统迁移:弱说明色 token text-ink-muted(#9A968E)取代 text-slate-400。
    expect(firstNo.className).toMatch(/text-ink-muted/);
  });

  test("4.7-UNIT-012: formats numeric capability per locale via Intl.NumberFormat", () => {
    // --- en locale ---
    renderWithIntl("en", enMessages);
    const enExpected = new Intl.NumberFormat("en").format(131072);
    // moonshot-v1-128k context_window_tokens = 131072 (BR-1.3 row 4).
    expect(screen.getAllByText(enExpected).length).toBeGreaterThan(0);

    // --- zh-CN locale ---
    // Re-render under zh-CN — accept Intl.NumberFormat('zh-CN') output as
    // the source of truth (CI Node ICU builds differ).
    document.body.innerHTML = "";
    renderWithIntl("zh-CN", zhMessages);
    const zhExpected = new Intl.NumberFormat("zh-CN").format(131072);
    expect(screen.getAllByText(zhExpected).length).toBeGreaterThan(0);
  });
});
