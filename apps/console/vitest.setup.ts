import '@testing-library/jest-dom/vitest';

// Recharts' <ResponsiveContainer> uses ResizeObserver, which jsdom does not
// implement. A no-op polyfill lets the chart mount under test (the SVG renders at
// 0×0 — harmless, since <UsageChart> tests assert on the hidden-table mirror and
// toggle, not the SVG geometry — Story 9.1b).
class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
globalThis.ResizeObserver = globalThis.ResizeObserver ?? (ResizeObserverStub as unknown as typeof ResizeObserver);
