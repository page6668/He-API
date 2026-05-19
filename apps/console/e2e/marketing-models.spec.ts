/**
 * Story 4.7 — `(marketing)/models` Playwright E2E + integration.
 *
 * Scenarios:
 *
 *   4.7-INT-004              — viewport-resize 375 px collapses to card stack
 *   4.7-INT-006              — fallback banner renders when /public/models 503
 *   4.7-E2E-001              — anonymous /en/models renders 11-row matrix
 *   4.7-E2E-002              — anonymous /zh-CN/models renders translated +
 *                              locale-formatted numbers
 *   4.7-E2E-003              — <head> includes title + description + og:* +
 *                              canonical (BR-2.5)
 *   4.7-E2E-004              — Lighthouse Performance regression marker
 *                              (BR-2.10 — SKIPPED in CI without
 *                              @unlighthouse/cli; non-blocking per Architect
 *                              advisory)
 *   4.7-BLIND-ERROR-001      — fallback banner when /public/models returns
 *                              malformed JSON (Zod safeParse failure)
 *   4.7-BLIND-FLOW-001       — no MISSING_MESSAGE console warnings on /en/models
 *   4.7-BLIND-FLOW-002       — crawler UA receives SSR HTML with 11 model rows
 *   4.7-BLIND-FLOW-003       — viewport boundary 767 / 768 px (Tailwind md:)
 *
 * Upstream is intercepted via `page.route` so the suite is self-contained
 * (no gateway dependency).
 */
import { test, expect, type Page } from "@playwright/test";

const CANONICAL_IDS = [
  "qwen-max",
  "qwen-plus",
  "deepseek-v3",
  "moonshot-v1-128k",
  "glm-4",
  "doubao-pro",
  "doubao-lite",
  "ernie-4.0",
  "he-router-cost",
  "he-router-quality",
  "he-router-latency",
];

const CAPABILITIES_FIXTURE = {
  object: "list" as const,
  data: CANONICAL_IDS.map((id) => ({
    id,
    object: "model" as const,
    created: 1700000000,
    owned_by: id.startsWith("qwen") ? "alibaba"
      : id.startsWith("deepseek") ? "deepseek"
      : id.startsWith("moonshot") ? "moonshot"
      : id.startsWith("glm") ? "zhipu"
      : id.startsWith("doubao") ? "bytedance"
      : id.startsWith("ernie") ? "baidu"
      : "he-api",
    capabilities: {
      chat: true,
      streaming: true,
      function_calling: id !== "doubao-lite",
      vision: false,
      json_mode: !(id === "doubao-pro" || id === "doubao-lite"),
      context_window_tokens: id === "moonshot-v1-128k" || id.startsWith("he-router") ? 131072
        : id === "deepseek-v3" ? 65536
        : id === "ernie-4.0" ? 8192
        : 32768,
      max_output_tokens: id === "doubao-lite" ? 4096
        : id === "ernie-4.0" ? 2048
        : 8192,
    },
  })),
};

async function stubPublicModels(page: Page, init?: { status?: number; body?: string }): Promise<void> {
  await page.route("**/public/models", async (route) => {
    if (init?.status) {
      await route.fulfill({ status: init.status, body: init.body ?? "" });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(CAPABILITIES_FIXTURE),
    });
  });
}

test.describe("Story 4.7 — (marketing)/models", () => {
  test("4.7-E2E-001: anonymous /en/models renders 11-row matrix", async ({ page }) => {
    await stubPublicModels(page);
    const resp = await page.goto("/en/models");
    expect(resp?.status()).toBe(200);

    const table = page.getByRole("table");
    await expect(table).toBeVisible();
    const rows = await table.getByRole("row").count();
    // 1 header + 11 model rows.
    expect(rows).toBe(12);

    for (const id of CANONICAL_IDS) {
      await expect(page.getByText(id, { exact: true }).first()).toBeVisible();
    }

    // Nav "Models" link is marked as current page.
    const modelsLink = page.getByRole("link", { name: /Models$/ });
    await expect(modelsLink).toHaveAttribute("aria-current", "page");
  });

  test("4.7-E2E-002: anonymous /zh-CN/models renders translated headings + locale-formatted numerics", async ({ page }) => {
    await stubPublicModels(page);
    await page.goto("/zh-CN/models");

    const heading = page.getByRole("heading", { level: 1 });
    await expect(heading).toHaveText("模型能力矩阵");

    const zhFormatted = new Intl.NumberFormat("zh-CN").format(131072);
    await expect(page.getByText(zhFormatted, { exact: false }).first()).toBeVisible();
  });

  test("4.7-E2E-003: <head> includes title + description + og:* + canonical", async ({ page }) => {
    await stubPublicModels(page);
    await page.goto("/en/models");

    await expect(page).toHaveTitle(/Model Capability Matrix/);

    const description = await page.locator('meta[name="description"]').getAttribute("content");
    expect(description).toBeTruthy();
    expect(description?.length).toBeGreaterThan(20);

    const ogTitle = await page.locator('meta[property="og:title"]').getAttribute("content");
    expect(ogTitle).toContain("Model Capability Matrix");

    const ogDescription = await page.locator('meta[property="og:description"]').getAttribute("content");
    expect(ogDescription).toBeTruthy();

    const canonical = await page.locator('link[rel="canonical"]').getAttribute("href");
    expect(canonical).toContain("/en/models");
  });

  test.skip("4.7-E2E-004: Lighthouse Performance ≥ 90 (regression marker)", () => {
    // SKIP-REASON: @unlighthouse/cli is not part of devDependencies in
    // Round 1 — Architect m-3 advisory notes Lighthouse is a non-blocking
    // regression marker. Future Story may add the harness; the SSR + minimal
    // client-side JS design should hit ≥ 90 trivially when measured.
  });

  test("4.7-INT-004: viewport-resize boundary 375 px collapses to mobile card stack", async ({ page }) => {
    await stubPublicModels(page);
    await page.setViewportSize({ width: 375, height: 800 });
    await page.goto("/en/models");

    const table = page.locator("table");
    await expect(table).toBeHidden();

    const articles = page.locator('article[role="region"]');
    await expect(articles.first()).toBeVisible();
    expect(await articles.count()).toBe(11);

    await page.setViewportSize({ width: 1024, height: 800 });
    await expect(table).toBeVisible();
    await expect(articles.first()).toBeHidden();
  });

  test("4.7-INT-006: fallback banner renders when /public/models returns 503", async ({ page }) => {
    await stubPublicModels(page, { status: 503, body: "service unavailable" });
    const resp = await page.goto("/en/models");
    // SEO-safe: page stays 200, banner replaces the matrix.
    expect(resp?.status()).toBe(200);

    const banner = page.getByTestId("models-fallback-banner");
    await expect(banner).toBeVisible();
    await expect(banner).toContainText("temporarily unavailable");

    await expect(page.locator("table tbody tr")).toHaveCount(0);
  });

  test("4.7-BLIND-ERROR-001: fallback banner renders when /public/models returns malformed JSON", async ({ page }) => {
    await stubPublicModels(page, {
      status: 200,
      body: '{"object":"list","data":[{"id":"qwen-max","object":"INVALID',
    });
    const resp = await page.goto("/en/models");
    expect(resp?.status()).toBe(200);

    const banner = page.getByTestId("models-fallback-banner");
    await expect(banner).toBeVisible();
    await expect(page.locator("table tbody tr")).toHaveCount(0);
  });

  test("4.7-BLIND-FLOW-001: no MISSING_MESSAGE console warnings during /en/models render", async ({ page }) => {
    const warnings: string[] = [];
    page.on("console", (msg) => {
      if (msg.text().includes("MISSING_MESSAGE")) {
        warnings.push(msg.text());
      }
    });
    await stubPublicModels(page);
    await page.goto("/en/models", { waitUntil: "networkidle" });

    expect(warnings, `next-intl MISSING_MESSAGE leaked:\n${warnings.join("\n")}`).toEqual([]);
  });

  test("4.7-BLIND-FLOW-002: SSR HTML for crawler UA contains 11 model rows", async ({ browser }) => {
    const context = await browser.newContext({ userAgent: "Googlebot/2.1" });
    const page = await context.newPage();
    await stubPublicModels(page);
    await page.goto("/en/models");
    const html = await page.content();
    for (const id of CANONICAL_IDS) {
      expect(html).toContain(id);
    }
    await context.close();
  });

  test("4.7-BLIND-FLOW-003: viewport boundary 767/768 px Tailwind md: edge", async ({ page }) => {
    await stubPublicModels(page);

    await page.setViewportSize({ width: 767, height: 800 });
    await page.goto("/en/models");
    await expect(page.locator("table")).toBeHidden();
    await expect(page.locator('article[role="region"]').first()).toBeVisible();

    await page.setViewportSize({ width: 768, height: 800 });
    await page.reload();
    await expect(page.locator("table")).toBeVisible();
    await expect(page.locator('article[role="region"]').first()).toBeHidden();
  });
});
