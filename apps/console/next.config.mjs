import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { createRequire } from 'node:module';
import createNextIntlPlugin from 'next-intl/plugin';

const require = createRequire(import.meta.url);
const __dirname = path.dirname(fileURLToPath(import.meta.url));

const withNextIntl = createNextIntlPlugin('./i18n/request.ts');

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  // Standalone server bundle for a minimal container image (Docker/K8s path).
  // 裸机部署走 `next start` + `pnpm deploy --prod`（见 deploy/bare-metal/build-console.sh），
  // 此时设 BARE_METAL_BUILD=1 关闭 standalone，避免 `next start` 不兼容警告。
  // 在 pnpm/Turbo monorepo 下，standalone 的 file-tracing root 必须是 repo root，
  // 才能把 workspace 依赖 (@he-api/i18n-keys) 追踪进 .next/standalone。
  ...(process.env.BARE_METAL_BUILD === '1'
    ? {}
    : { output: 'standalone', outputFileTracingRoot: path.join(__dirname, '../../') }),
  // @he-api/i18n-keys is consumed as TS source (main = src/index.ts).
  transpilePackages: ['@he-api/i18n-keys'],
  // Staging deploy: don't let a lint warning / type nit block the image build.
  // Quality gates run in the dedicated lint/typecheck CI jobs, not here.
  eslint: { ignoreDuringBuilds: true },
  typescript: { ignoreBuildErrors: true },
};

// bundle-analyzer 仅在 ANALYZE=true 时启用，避免生产运行时（含 pnpm deploy --prod）
// 依赖 @next/bundle-analyzer 这个 devDependency，导致 next start 启动失败。
let config = withNextIntl(nextConfig);
if (process.env.ANALYZE === 'true') {
  const bundleAnalyzer = require('@next/bundle-analyzer');
  config = bundleAnalyzer({ enabled: true })(config);
}

export default config;
