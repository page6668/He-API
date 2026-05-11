export const locales = ['en', 'zh-CN', 'ja', 'ko', 'es', 'fr', 'de', 'pt', 'ru', 'ar'] as const;

export type Locale = typeof locales[number];

export const defaultLocale: Locale = 'en';

export const fallbackMap: Readonly<Record<string, Locale>> = {
  'zh-TW': 'zh-CN',
  'zh-HK': 'zh-CN',
  'pt-BR': 'pt',
  'pt-PT': 'pt',
};

export const isLocale = (value: unknown): value is Locale =>
  typeof value === 'string' && (locales as readonly string[]).includes(value);

export const COOKIE_NAME = 'he_locale';
