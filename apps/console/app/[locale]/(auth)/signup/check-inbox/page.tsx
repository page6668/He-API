import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale } from '@/i18n/config';

interface CheckInboxPageProps {
  params: { locale: string };
  searchParams: { email?: string };
}

export default async function CheckInboxPage({
  params: { locale },
  searchParams: { email },
}: CheckInboxPageProps) {
  const resolvedLocale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  // 命名空间 = 文件名:messages/<locale>/auth.json → `auth`;文案在 auth.signupCheckInbox.*。
  // 无参 getTranslations() 会 MISSING_MESSAGE 并让本页 500。
  const t = await getTranslations('auth');

  // `email` is the masked form coming from the registerUser redirect. We
  // never trust it as-is because anyone can hand-craft this URL — but
  // displaying it back as-is is fine since we render with React's automatic
  // string escaping and the value is plain text inside a <p>.
  const maskedEmail = typeof email === 'string' && email.length > 0 ? email : '';

  return (
    // 左对齐编辑式排版(design-system.md layout.alignment:禁止 everything-centered)。
    <section aria-labelledby="check-inbox-title" className="space-y-4">
      <h1 id="check-inbox-title" className="text-h2 text-ink">
        {t('signupCheckInbox.title')}
      </h1>
      <p className="text-body text-ink-secondary">
        {t('signupCheckInbox.description', { email: maskedEmail })}
      </p>
      {/* 内嵌读数区:1px 暖边框 + 明度差建层级,零阴影。 */}
      <div className="rounded-lg border border-line bg-surface-sunken p-4 text-small text-ink-secondary">
        {t('signupCheckInbox.supportNote')}
      </div>
    </section>
  );
}
