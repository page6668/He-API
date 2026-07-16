import { unstable_setRequestLocale, getTranslations } from 'next-intl/server';

import { isLocale, defaultLocale, type Locale } from '@/i18n/config';
import { verifyEmailAction, type VerifyEmailResult } from '@/app/[locale]/_actions/auth';

import { ResendModal } from '../signup/check-inbox/ResendModal';

interface VerifyEmailPageProps {
  params: { locale: string };
  searchParams: { token?: string };
}

/**
 * 主操作 CTA(朱砂)。这里是链接而非 <button>,所以不能直接用 kit 的 <Button>;
 * 类名与 kit 的 primary variant 保持一致(8/6 圆角、duration-state ease-he)。
 */
const sealCtaCls =
  'inline-block rounded-md bg-seal px-4 py-2 text-small font-medium text-white transition-colors duration-state ease-he hover:bg-seal-hover focus:outline-none focus:ring-2 focus:ring-seal/30';

/**
 * VerifyEmailPage is a Server Component that calls the verifyEmailAction
 * Server Action synchronously on render. That way users can complete email
 * verification by clicking the email link with JavaScript disabled — the
 * page renders with the verified state on the very first GET.
 *
 * (UNIT-102 + AC2 UI Interaction explicitly require this — the path
 * must work without client-side JS.)
 */
export default async function VerifyEmailPage({
  params: { locale },
  searchParams: { token },
}: VerifyEmailPageProps) {
  const resolvedLocale: Locale = isLocale(locale) ? locale : defaultLocale;
  unstable_setRequestLocale(resolvedLocale);

  // 命名空间 = 文件名:messages/<locale>/auth.json → `auth`;文案在 auth.verifyEmail.*。
  // 无参 getTranslations() 会 MISSING_MESSAGE 并让本页 500。
  const t = await getTranslations('auth');

  // Pass an empty string when ?token=… is missing — the zod schema in the
  // action surfaces this as an invalidToken error, which the page then
  // shows alongside the resend CTA.
  const result: VerifyEmailResult = await verifyEmailAction({ token: token ?? '' });

  if (result.ok && result.status === 'email_verified') {
    return (
      // 左对齐编辑式排版;唯一的朱砂落在「登录」CTA(铁律2)。
      <section aria-labelledby="verify-success" className="space-y-4">
        <h1 id="verify-success" className="text-h2 text-ink">
          {t('verifyEmail.successTitle')}
        </h1>
        <p className="text-body text-ink-secondary">{t('verifyEmail.successDescription')}</p>
        <a href={`/${resolvedLocale}/signin`} className={sealCtaCls}>
          {t('verifyEmail.successCta')}
        </a>
      </section>
    );
  }

  if (result.ok && result.status === 'already_verified') {
    return (
      <section aria-labelledby="verify-already" className="space-y-4">
        <h1 id="verify-already" className="text-h2 text-ink">
          {t('verifyEmail.alreadyVerifiedTitle')}
        </h1>
        <p className="text-body text-ink-secondary">
          {t('verifyEmail.alreadyVerifiedDescription')}
        </p>
        <a href={`/${resolvedLocale}/signin`} className={sealCtaCls}>
          {t('verifyEmail.alreadyVerifiedCta')}
        </a>
      </section>
    );
  }

  // Error path — token invalid / expired / used. The resend modal lets the
  // user request a fresh link (the 410_token_used path can't be recovered
  // by resend either, but the UX is the same — try again).
  const errorTitleKey =
    result.ok === false && result.code === 'auth.errors.tokenAlreadyUsed'
      ? 'verifyEmail.tokenUsedTitle'
      : 'verifyEmail.tokenExpiredTitle';
  const errorBodyKey =
    result.ok === false && result.code === 'auth.errors.tokenAlreadyUsed'
      ? 'verifyEmail.tokenUsedDescription'
      : 'verifyEmail.tokenExpiredDescription';

  return (
    // 错误态:一行安静说明 + 重发入口,不要满宽红底 banner
    // (design-system.md key_page_direction.dashboard 同调)。
    <section aria-labelledby="verify-error" className="space-y-4">
      <h1 id="verify-error" className="text-h2 text-ink">
        {t(errorTitleKey)}
      </h1>
      <p className="text-body text-ink-secondary">{t(errorBodyKey)}</p>
      <ResendModal />
    </section>
  );
}
