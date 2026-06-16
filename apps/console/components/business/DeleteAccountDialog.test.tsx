// Story 2.7 AC1 — DeleteAccountDialog unit/integration tests (replaces the QA
// skeleton). Covers re-auth field branching, disabled-until-valid gating, the
// alertdialog posture, pending-CTA disable, and Server-Action wiring
// (200→recovery redirect, 403→inline error, 429→toast).
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NextIntlClientProvider } from 'next-intl';

import { DeleteAccountDialog } from './DeleteAccountDialog';
import enAccount from '@/messages/en/account.json';
import type { DeletionState } from '@/lib/account/deletion-actions';

const pushMock = vi.fn();
vi.mock('next/navigation', () => ({
  useRouter: () => ({ push: pushMock, refresh: vi.fn() }),
}));

const requestMock = vi.fn();
vi.mock('@/lib/account/deletion-actions', () => ({
  requestAccountDeletion: (...args: unknown[]) => requestMock(...args),
}));

function renderDialog(state: Partial<DeletionState>, email = 'user@example.com') {
  const full: DeletionState = {
    status: 'active',
    has_password: true,
    totp_enabled: false,
    timezone: 'UTC',
    pending_deletion_at: null,
    ...state,
  };
  return render(
    <NextIntlClientProvider locale="en" messages={{ account: enAccount }}>
      <DeleteAccountDialog state={full} email={email} locale="en" />
    </NextIntlClientProvider>,
  );
}

async function openDialog(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', { name: 'Delete My Account' }));
}

beforeEach(() => {
  pushMock.mockReset();
  requestMock.mockReset();
});

describe('DeleteAccountDialog', () => {
  // 2.7-UNIT-001
  it('renders a password input when has_password=true', async () => {
    const user = userEvent.setup();
    renderDialog({ has_password: true });
    await openDialog(user);
    expect(screen.getByLabelText(/password/i)).toHaveAttribute('type', 'password');
  });

  // 2.7-UNIT-002
  it('renders a type-exact-email confirm when OAuth-only (has_password=false)', async () => {
    const user = userEvent.setup();
    renderDialog({ has_password: false });
    await openDialog(user);
    expect(screen.getByLabelText(/email/i)).toBeInTheDocument();
  });

  // 2.7-UNIT-003
  it('renders an additional TOTP input when totp_enabled=true', async () => {
    const user = userEvent.setup();
    renderDialog({ has_password: true, totp_enabled: true });
    await openDialog(user);
    expect(screen.getByLabelText(/password/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/authentication code/i)).toBeInTheDocument();
  });

  // 2.7-UNIT-004 / BLIND-BOUNDARY-001
  it('keeps Confirm disabled until required fields are satisfied', async () => {
    const user = userEvent.setup();
    renderDialog({ has_password: true });
    await openDialog(user);
    const confirm = screen.getByRole('button', { name: 'Delete my account' });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText(/password/i), 'hunter2');
    expect(confirm).toBeEnabled();
  });

  // 2.7-UNIT-005 — email confirm enables Confirm only on exact match.
  it('enables Confirm only on exact-email match for OAuth-only', async () => {
    const user = userEvent.setup();
    renderDialog({ has_password: false }, 'user@example.com');
    await openDialog(user);
    const confirm = screen.getByRole('button', { name: 'Delete my account' });
    const field = screen.getByLabelText(/email/i);
    await user.type(field, 'WRONG@example.com');
    expect(confirm).toBeDisabled();
    await user.clear(field);
    await user.type(field, 'user@example.com');
    expect(confirm).toBeEnabled();
  });

  // 2.7-UNIT-007
  it('opens as an alertdialog', async () => {
    const user = userEvent.setup();
    renderDialog({ has_password: true });
    await openDialog(user);
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
  });

  // 2.7-UNIT-010
  it('disables the CTA with a tooltip when already pending_deletion', () => {
    renderDialog({ status: 'pending_deletion' });
    const cta = screen.getByRole('button', { name: 'Delete My Account' });
    expect(cta).toBeDisabled();
    expect(cta).toHaveAttribute('title', enAccount.delete.cta.disabled_pending);
  });

  // 2.7-INT-001
  it('redirects to the recovery page on a 200 response', async () => {
    const user = userEvent.setup();
    requestMock.mockResolvedValue({ kind: 'ok' });
    renderDialog({ has_password: true });
    await openDialog(user);
    await user.type(screen.getByLabelText(/password/i), 'hunter2');
    await user.click(screen.getByRole('button', { name: 'Delete my account' }));
    await waitFor(() => expect(pushMock).toHaveBeenCalledWith('/en/account/recovery'));
    expect(requestMock).toHaveBeenCalledWith({ password: 'hunter2', confirm_email: undefined, totp_code: undefined });
  });

  // 2.7-INT-002
  it('keeps the dialog open with an inline error on 403_bad_reauth', async () => {
    const user = userEvent.setup();
    requestMock.mockResolvedValue({ kind: 'bad_reauth' });
    renderDialog({ has_password: true });
    await openDialog(user);
    await user.type(screen.getByLabelText(/password/i), 'wrong');
    await user.click(screen.getByRole('button', { name: 'Delete my account' }));
    await screen.findByText(enAccount.delete.dialog.errors.bad_password);
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
    expect(pushMock).not.toHaveBeenCalled();
  });

  // 2.7-INT-003
  it('shows a rate-limited toast on 429', async () => {
    const user = userEvent.setup();
    requestMock.mockResolvedValue({ kind: 'rate_limited' });
    renderDialog({ has_password: true });
    await openDialog(user);
    await user.type(screen.getByLabelText(/password/i), 'hunter2');
    await user.click(screen.getByRole('button', { name: 'Delete my account' }));
    await screen.findByText(enAccount.delete.errors.rate_limited);
    expect(screen.getByRole('alertdialog')).toBeInTheDocument();
  });

  // 2.7-UNIT-008 — Cancel closes with no side effect.
  it('closes on Cancel without calling the action', async () => {
    const user = userEvent.setup();
    renderDialog({ has_password: true });
    await openDialog(user);
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument());
    expect(requestMock).not.toHaveBeenCalled();
  });
});
