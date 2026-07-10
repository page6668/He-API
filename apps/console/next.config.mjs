import { fileURLToPath } from 'node:url';
import path from 'node:path';
import createNextIntlPlugin from 'next-intl/plugin';
import bundleAnalyzer from '@next/bundle-analyzer';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const withNextIntl = createNextIntlPlugin('./i18n/request.ts');

const withBundleAnalyzer = bundleAnalyzer({
  enabled: process.env.ANALYZE === 'true',
});

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  // Standalone server bundle for a minimal container image. In the pnpm/Turbo
  // monorepo the file-tracing root MUST be the repo root so workspace deps
  // (@he-api/i18n-keys) are traced into .next/standalone.
  output: 'standalone',
  outputFileTracingRoot: path.join(__dirname, '../../'),
  // @he-api/i18n-keys is consumed as TS source (main = src/index.ts).
  transpilePackages: ['@he-api/i18n-keys'],
  // Staging deploy: don't let a lint warning / type nit block the image build.
  // Quality gates run in the dedicated lint/typecheck CI jobs, not here.
  eslint: { ignoreDuringBuilds: true },
  typescript: { ignoreBuildErrors: true },
};

export default withBundleAnalyzer(withNextIntl(nextConfig));
