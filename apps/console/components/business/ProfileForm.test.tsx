// Story 2.5 — ProfileForm unit tests (Vitest + Testing Library).
//
// Covers QA scenarios 2.5-UNIT-011..016 (selected): defaults render, dirty
// detection, 2FA + auth-method badges, locale dropdown native labels.
//
// E2E tests in apps/console/e2e/2.5-profile-management.spec.ts cover the
// full submit-and-redirect flow; here we focus on component behaviour
// without the network round-trip.

import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { NextIntlClientProvider } from 'next-intl';

import { ProfileForm } from './ProfileForm';
import enMessages from '@/messages/en/account.json';
import type { Profile } from '@/app/[locale]/(console)/settings/profile/_actions/get-my-profile';

// Wrap render in next-intl provider so useTranslations() resolves.
function renderWithIntl(ui: React.ReactNode) {
  return render(
    <NextIntlClientProvider locale="en" messages={{ account: enMessages }}>
      {ui}
    </NextIntlClientProvider>,
  );
}

// Mock the update-my-profile action so we don't actually fire HTTP in tests.
vi.mock('@/app/[locale]/(console)/settings/profile/_actions/update-my-profile', () => ({
  updateMyProfile: vi.fn(async () => ({ kind: 'ok', localeChanged: false, etag: '"new"' })),
}));

const baseProfile: Profile = {
  user_id: '11111111-1111-1111-1111-111111111111',
  email: 'user@example.com',
  display_name: 'Alice',
  locale: 'en',
  timezone: 'UTC',
  totp_enabled: false,
  oauth_provider: null,
  created_at: '2026-05-15T00:00:00Z',
  updated_at: '2026-05-16T10:00:00Z',
};

describe('ProfileForm', () => {
  it('renders all default values from props', () => {
    renderWithIntl(<ProfileForm defaults={baseProfile} etag={`"123"`} currentLocale="en" />);

    expect(screen.getByLabelText(/Display name/i)).toHaveValue('Alice');
    expect(screen.getByLabelText(/Email/i)).toHaveValue('user@example.com');
    expect(screen.getByLabelText(/Email/i)).toHaveAttribute('readOnly');
    expect(screen.getByLabelText(/Language/i)).toHaveValue('en');
    expect(screen.getByLabelText(/Time zone/i)).toHaveValue('UTC');
  });

  it('Save button is disabled when form is not dirty', () => {
    renderWithIntl(<ProfileForm defaults={baseProfile} etag={`"123"`} currentLocale="en" />);
    expect(screen.getByRole('button', { name: /Save changes/i })).toBeDisabled();
  });

  it('renders the 2FA disabled badge when totp_enabled=false', () => {
    renderWithIntl(<ProfileForm defaults={{ ...baseProfile, totp_enabled: false }} etag={`"123"`} currentLocale="en" />);
    expect(screen.getByText(/Two-factor authentication not enabled/i)).toBeInTheDocument();
  });

  it('renders the 2FA enabled badge when totp_enabled=true', () => {
    renderWithIntl(<ProfileForm defaults={{ ...baseProfile, totp_enabled: true }} etag={`"123"`} currentLocale="en" />);
    expect(screen.getByText(/Two-factor authentication enabled/i)).toBeInTheDocument();
  });

  it('renders the Google auth-method badge when oauth_provider=google', () => {
    renderWithIntl(<ProfileForm defaults={{ ...baseProfile, oauth_provider: 'google' }} etag={`"123"`} currentLocale="en" />);
    expect(screen.getByText(/Signed in with Google/i)).toBeInTheDocument();
  });

  it('renders the email+password badge when oauth_provider=null', () => {
    renderWithIntl(<ProfileForm defaults={baseProfile} etag={`"123"`} currentLocale="en" />);
    expect(screen.getByText(/Signed in with email and password/i)).toBeInTheDocument();
  });

  it('locale Select lists all 10 MVP locales with native labels', () => {
    renderWithIntl(<ProfileForm defaults={baseProfile} etag={`"123"`} currentLocale="en" />);
    const select = screen.getByLabelText(/Language/i);
    const options = Array.from(select.querySelectorAll('option')).map((o) => o.textContent);
    expect(options).toEqual([
      'English',
      '中文',
      '日本語',
      '한국어',
      'Español',
      'Français',
      'Deutsch',
      'Português',
      'Русский',
      'العربية',
    ]);
  });
});
