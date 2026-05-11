// Package templates loads per-locale email templates from the embedded
// templates/email_verification/ tree.
//
// Layout (per BR-2.6 + Story 2.1 i18n locale set):
//
//   internal/templates/email_verification/{locale}.html
//   internal/templates/email_verification/{locale}.txt
//
// where {locale} ∈ {en, zh-CN, ja, ko, es, fr, de, pt, ru, ar}. P1 ships the
// English variants only; the other 9 are stubbed with `<!-- TODO: localize -->`
// markers per the same async-translation strategy as Story 2.1 AC4 (real
// translations are a separate localization workstream and do not block Dev).
//
// Variables expected on EMAIL_VERIFICATION: {{.Token}}, {{.VerificationLink}}.
// Template rendering moves to P2 (T1, AC1).
package templates
