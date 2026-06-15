// Story 10.7 AC2 — IntercomMessenger client component (jsdom).
//
// QA scenarios: INT-011 (authed mount + boot), BLIND-ERROR-010 (snippet load
// failure → graceful degrade, no crash), BLIND-FLOW-010 (logout shutdown/re-boot
// hygiene on a shared device), BLIND-FLOW-011 (mid-session locale switch).

import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { IntercomMessenger } from './IntercomMessenger';

interface IntercomGlobal extends Window {
  Intercom?: ((...args: unknown[]) => void) & { calls?: unknown[][] };
  intercomSettings?: Record<string, unknown>;
}

function installFakeIntercom() {
  const calls: unknown[][] = [];
  const fn = (...args: unknown[]) => {
    calls.push(args);
  };
  (fn as { calls?: unknown[][] }).calls = calls;
  (window as IntercomGlobal).Intercom = fn as IntercomGlobal['Intercom'];
  return calls;
}

afterEach(() => {
  cleanup();
  delete (window as IntercomGlobal).Intercom;
  delete (window as IntercomGlobal).intercomSettings;
  vi.restoreAllMocks();
});

describe('10.7-INT-011 IntercomMessenger boots on the authed console face', () => {
  it('boots with a zero-PII payload when given an app_id and a non-PRC region', () => {
    const calls = installFakeIntercom();
    render(<IntercomMessenger appId="app_pub_123" locale="zh-CN" countryCode="US" />);
    const boot = calls.find((c) => c[0] === 'boot');
    expect(boot).toBeTruthy();
    const payload = boot![1] as Record<string, unknown>;
    expect(payload).toEqual({ app_id: 'app_pub_123', language_override: 'zh-CN' });
    // hard zero-PII: no identity fields ever
    expect(JSON.stringify(payload)).not.toMatch(/email|user_id|user_hash|name/);
  });

  it('UNIT-013 — does NOT boot for a PRC-region request', () => {
    const calls = installFakeIntercom();
    render(<IntercomMessenger appId="app_pub_123" locale="zh-CN" countryCode="CN" />);
    expect(calls.find((c) => c[0] === 'boot')).toBeUndefined();
  });

  it('BOUNDARY-010 — missing app_id → no boot, no crash', () => {
    const calls = installFakeIntercom();
    expect(() => render(<IntercomMessenger appId={undefined} locale="en" countryCode="US" />)).not.toThrow();
    expect(calls.find((c) => c[0] === 'boot')).toBeUndefined();
  });
});

describe('10.7-BLIND-ERROR-010 Intercom unavailable → graceful degrade', () => {
  it('does not throw when window.Intercom is absent (snippet blocked / failed)', () => {
    delete (window as IntercomGlobal).Intercom;
    expect(() => render(<IntercomMessenger appId="app_pub_123" locale="en" countryCode="US" />)).not.toThrow();
  });
});

describe('10.7-BLIND-FLOW-011 mid-session locale switch', () => {
  it('updates language_override without a full reload', () => {
    const calls = installFakeIntercom();
    const { rerender } = render(<IntercomMessenger appId="app_pub_123" locale="en" countryCode="US" />);
    rerender(<IntercomMessenger appId="app_pub_123" locale="ja" countryCode="US" />);
    const update = calls.find((c) => c[0] === 'update' && (c[1] as Record<string, unknown>)?.language_override === 'ja');
    expect(update).toBeTruthy();
  });
});

describe('10.7-BLIND-FLOW-010 logout / unmount hygiene', () => {
  it('shuts the Messenger down on unmount so a shared device does not leak the session', () => {
    const calls = installFakeIntercom();
    const { unmount } = render(<IntercomMessenger appId="app_pub_123" locale="en" countryCode="US" />);
    unmount();
    expect(calls.find((c) => c[0] === 'shutdown')).toBeTruthy();
  });
});
