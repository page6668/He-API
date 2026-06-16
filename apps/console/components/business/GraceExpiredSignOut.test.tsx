// Story 2.7 AC3 — GraceExpiredSignOut tests. This invisible component is
// rendered alongside the {recovery.expired} notice on the recovery page's
// grace-expired branch; on mount it MUST terminate the orphaned session exactly
// once (QA-2.7-001), satisfying the AC3 "expired message + signout" requirement.
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/react';

import { GraceExpiredSignOut } from './GraceExpiredSignOut';

const signOutMock = vi.fn();
vi.mock('@/lib/account/deletion-actions', () => ({
  signOutAfterGraceExpiry: (...args: unknown[]) => signOutMock(...args),
}));

beforeEach(() => {
  signOutMock.mockReset();
});

describe('GraceExpiredSignOut', () => {
  // 2.7-UNIT-012 — auto signout fires on mount with the page locale.
  it('signs out on mount with the given locale', () => {
    render(<GraceExpiredSignOut locale="en" />);
    expect(signOutMock).toHaveBeenCalledTimes(1);
    expect(signOutMock).toHaveBeenCalledWith('en');
  });

  // 2.7-UNIT-013 — renders no visible UI (the notice is owned by the page).
  it('renders nothing of its own', () => {
    const { container } = render(<GraceExpiredSignOut locale="fr" />);
    expect(container).toBeEmptyDOMElement();
  });
});
