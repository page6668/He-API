/**
 * Story 4.7 — `(marketing)/models` accessibility scan (4.7-INT-005).
 *
 * Skipped in Round 1 because `@axe-core/playwright` is not part of the
 * console devDependencies. The test scaffold is preserved so the harness
 * can be added in a future Story without rewriting test bodies — replace
 * the `test.skip` with the implementation commented below.
 *
 * BR-2.7 WCAG 2.1 AA — the matrix carries a semantic <table> with caption,
 * scope=col / scope=row headers, visually-hidden text on every ✓/✗ badge,
 * and locale-formatted numeric cells. axe-core will gate this contract in
 * the follow-up Story that adds the harness.
 */
import { test } from "@playwright/test";

test.describe("Story 4.7 — accessibility scan", () => {
  test.skip("4.7-INT-005: axe-core zero violations on /en/models", async () => {
    // SKIP-REASON: @axe-core/playwright not yet installed in apps/console.
    // Architect m-1 advisory notes the a11y scan is a follow-up Story.
    //
    // Implementation (uncomment + add `@axe-core/playwright` devDep):
    //
    //   const { default: AxeBuilder } = await import("@axe-core/playwright");
    //   await page.goto("/en/models");
    //   const results = await new AxeBuilder({ page })
    //     .withTags(["wcag2a", "wcag2aa", "wcag21aa"])
    //     .analyze();
    //   expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
  });

  test.skip("4.7-INT-005: axe-core zero violations on /zh-CN/models", async () => {
    // SKIP-REASON: same as above. Add when harness lands.
  });
});
