// Story 2.7 AC3 — ReactivateAccountButton unit/integration tests. Focus on the
// grace-expired (410) path required by the AC3 error-table: it MUST sign the
// user out (terminate the session), not merely surface a notice (QA-2.7-001).
// Also covers the happy reactivate→console and unauthorized→signin branches.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NextIntlClientProvider } from 'next-intl';

import { ReactivateAccountButton } from './ReactivateAccountButton';
import enAccount from '@/messages/en/account.json';

const pushMock = vi.fn();
const refreshMock = vi.fn();
vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: pushMock, refresh: refreshMock }),
}));

const cancelMock = vi.fn();
const signOutMock = vi.fn();
vi.mock('@/lib/account/deletion-actions', () => ({
  cancelAccountDeletion: (...args: unknown[]) => cancelMock(...args),
  signOutAfterGraceExpiry: (...args: unknown[]) => signOutMock(...args),
}));

function renderButton() {
  return render(
    <NextIntlClientProvider locale="en" messages={{ account: enAccount }}>
      <ReactivateAccountButton locale="en" />
    </NextIntlClientProvider>,
  );
}

function clickReactivate(user: ReturnType<typeof userEvent.setup>) {
  return user.click(screen.getByRole('button', { name: enAccount.delete.recovery.reactivate_cta }));
}

beforeEach(() => {
  pushMock.mockReset();
  refreshMock.mockReset();
  cancelMock.mockReset();
  signOutMock.mockReset();
});

describe('ReactivateAccountButton', () => {
  // 2.7-INT-004 — happy path: reactivate → back to console.
  it('returns to the console on a 200 (ok) cancel', async () => {
    const user = userEvent.setup();
    cancelMock.mockResolvedValue({ kind: 'ok' });
    renderButton();
    await clickReactivate(user);
    await waitFor(() => expect(pushMock).toHaveBeenCalledWith('/en/'));
    expect(signOutMock).not.toHaveBeenCalled();
  });

  // 2.7-INT-005 — grace expired (410) MUST terminate the session, not just notify.
  it('signs the user out when the grace window has already expired (410)', async () => {
    const user = userEvent.setup();
    cancelMock.mockResolvedValue({ kind: 'grace_expired' });
    renderButton();
    await clickReactivate(user);
    await screen.findByText(enAccount.delete.recovery.expired);
    await waitFor(() => expect(signOutMock).toHaveBeenCalledWith('en'));
    expect(pushMock).not.toHaveBeenCalledWith('/en/');
  });

  // 2.7-INT-006 — 401 routes to signin without a session-clearing signout.
  it('routes to signin on unauthorized', async () => {
    const user = userEvent.setup();
    cancelMock.mockResolvedValue({ kind: 'unauthorized' });
    renderButton();
    await clickReactivate(user);
    await waitFor(() => expect(pushMock).toHaveBeenCalledWith('/en/signin'));
    expect(signOutMock).not.toHaveBeenCalled();
  });

  // 2.7-UNIT-011 — a generic error surfaces a notice but does NOT sign out
  // (transient failures must not destroy the session).
  it('does not sign out on a generic error', async () => {
    const user = userEvent.setup();
    cancelMock.mockResolvedValue({ kind: 'error' });
    renderButton();
    await clickReactivate(user);
    await screen.findByText(enAccount.delete.recovery.expired);
    expect(signOutMock).not.toHaveBeenCalled();
  });
});
