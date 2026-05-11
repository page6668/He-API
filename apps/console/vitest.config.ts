import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import { resolve } from 'node:path';

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': resolve(__dirname, '.'),
      '@he-api/i18n-keys': resolve(__dirname, '../../packages/i18n-keys/src/index.ts'),
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./vitest.setup.ts'],
    include: ['__tests__/**/*.test.ts', '__tests__/**/*.test.tsx', 'components/**/*.test.tsx', 'app/**/*.test.tsx'],
    exclude: ['node_modules', 'e2e', '.next'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'lcov'],
      thresholds: {
        lines: 80,
        functions: 80,
        branches: 80,
        statements: 80,
      },
      include: ['app/**', 'components/**', 'lib/**', 'i18n/**'],
      exclude: ['**/*.test.ts', '**/*.test.tsx', '**/__tests__/**'],
    },
  },
});
