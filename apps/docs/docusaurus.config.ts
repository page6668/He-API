import type { Config } from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';
import { themes as prismThemes } from 'prism-react-renderer';

/**
 * He-API public documentation site (Story 10.5).
 *
 * Framework: Docusaurus 3.x — Architect ruling OQ-10.5-1 (self-hosted posture
 * rules out Mintlify SaaS; native i18n + MDX + RTL + SSG export beats hand-built
 * Next.js docs). Presentation-only: transcribes rest-api-spec.md §5.1 (API
 * Reference SoT) + cites the 3 delivered SDKs (10.2/10.3/10.4). No gateway/proto/DB/SDK changes.
 *
 * Deployment (OQ-10.5-2): static SSG export → CDN (NOT CF Workers content cache).
 * Production docs.he-api.com DNS cutover + monitoring = Story 10.7. 10.5 ships a
 * buildable + staging-accessible site with build/link-check/completeness gates GREEN.
 */

// 10 locales — same set as the console (10.1); BR-10.5.7 (no zh-TW/hi/id in this story).
const LOCALES = ['en', 'zh-CN', 'ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar'] as const;

const config: Config = {
  title: 'He-API',
  tagline: 'OpenAI-compatible LLM gateway — Quickstart, API Reference, Cookbook',
  favicon: 'img/favicon.svg',

  // OQ-10.5-2: production DNS cutover is Story 10.7; staging-accessible satisfies AC1 "可访问".
  url: 'https://docs.he-api.com',
  baseUrl: '/',

  organizationName: 'he-api',
  projectName: 'docs',

  // Link-check gate (AC1 / INT-002): a dead internal link or anchor FAILS the build.
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  onDuplicateRoutes: 'throw',

  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: [...LOCALES],
    localeConfigs: {
      en: { label: 'English', direction: 'ltr', htmlLang: 'en' },
      'zh-CN': { label: '简体中文', direction: 'ltr', htmlLang: 'zh-CN' },
      ja: { label: '日本語', direction: 'ltr', htmlLang: 'ja' },
      ko: { label: '한국어', direction: 'ltr', htmlLang: 'ko' },
      es: { label: 'Español', direction: 'ltr', htmlLang: 'es' },
      fr: { label: 'Français', direction: 'ltr', htmlLang: 'fr' },
      de: { label: 'Deutsch', direction: 'ltr', htmlLang: 'de' },
      pt: { label: 'Português', direction: 'ltr', htmlLang: 'pt' },
      ru: { label: 'Русский', direction: 'ltr', htmlLang: 'ru' },
      // BR-10.5.10: Arabic is RTL. Code blocks / API paths / package names stay LTR
      // via the `.he-ltr` island + Docusaurus `dir="ltr"` on <code> (src/css/custom.css).
      ar: { label: 'العربية', direction: 'rtl', htmlLang: 'ar' },
    },
  },

  presets: [
    [
      'classic',
      {
        docs: {
          // Docs-as-site: home (/) + the three MUST sections live directly under root.
          routeBasePath: '/',
          sidebarPath: './sidebars.ts',
          editUrl: undefined,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      defaultMode: 'dark',
      respectPrefersColorScheme: true,
    },
    // Story 10.8 AC2 (§9.2) — outward "Beta" marking on the docs site during the
    // Beta period, coordinated with the console BetaBadge. Removed at GA together
    // with the console badge (go-live-checklist "drop Beta badge" step, BR-10.8.11).
    announcementBar: {
      id: 'beta',
      content: 'He-API is in public <strong>Beta</strong> — APIs and limits may change.',
      isCloseable: false,
    },
    navbar: {
      title: 'He-API',
      items: [
        { to: '/quickstart', label: 'Quickstart', position: 'left' },
        { to: '/api-reference', label: 'API Reference', position: 'left' },
        { to: '/cookbook', label: 'Cookbook', position: 'left' },
        { to: '/sdk', label: 'SDKs', position: 'left' },
        // Language switcher — stays on the current page's locale route (BR-10.5.11).
        { type: 'localeDropdown', position: 'right' },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            { label: 'Quickstart', to: '/quickstart' },
            { label: 'API Reference', to: '/api-reference' },
            { label: 'Cookbook', to: '/cookbook' },
            { label: 'SDKs', to: '/sdk' },
          ],
        },
      ],
      copyright: `He-API — OpenAI-compatible LLM gateway.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'python', 'go', 'json'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
