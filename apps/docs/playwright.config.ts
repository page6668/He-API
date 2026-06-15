import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright config for the He-API docs site (Story 10.5, AC1/AC2 E2E).
 *
 * The site is a presentation-only static SSG bundle; RTL behaviour is CSS-driven and
 * consistent across engines, and several cases are build-time/file gates that must not
 * multiply across browsers — so we run a single chromium project (console e2e uses three
 * because it is an interactive app). The webServer serves the already-built `build/` dir
 * (turbo `test:e2e` dependsOn `build`).
 */
const PORT = process.env.PLAYWRIGHT_PORT ?? '3011';
const BASE_URL = process.env.PLAYWRIGHT_BASE_URL ?? `http://127.0.0.1:${PORT}`;

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  timeout: 60_000,
  reporter: [
    ['html', { open: 'never' }],
    ['list'],
  ],
  use: {
    baseURL: BASE_URL,
    trace: 'on-first-retry',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `pnpm exec docusaurus serve --dir build --port ${PORT} --host 127.0.0.1 --no-open`,
    url: BASE_URL,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
