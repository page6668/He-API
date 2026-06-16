'use client';

// Story 6.5 AC1 — account-level default routing-strategy form (client component).
//
// Mirrors the Story-2.5 ProfileForm pattern: useTransition, dirty detection,
// discriminated-union Server Action result, success/error banners, and every
// string resolved from the `account` i18n namespace (no hard-coded copy).
//
// The 4 choices map to the persisted value (BR1-2):
//   passthrough → null ("no default" → STRATEGY_DEFAULT passthrough)
//   quality | cost | latency → the literal string
//
// On save the gateway re-resolves the default on the NEXT chat request via the
// sentinel-invalidated hot-path cache (Q-A Option B) — there is no client-side
// staleness copy (the overruled Option A's "next session refresh" framing is
// gone per the Architect Low-issue ruling).

import { useState, useTransition, type FormEvent } from 'react';
import { useTranslations } from 'next-intl';

import {
  updateMyRoutingStrategy,
  type RoutingStrategyValue,
  type UpdateRoutingStrategyResult,
} from '@/app/[locale]/(console)/settings/profile/_actions/update-my-routing-strategy';

// The 4 selectable choices. 'passthrough' is the UI token for "no default".
const CHOICES = ['passthrough', 'quality', 'cost', 'latency'] as const;
type Choice = (typeof CHOICES)[number];

// Map a persisted wire value (string | null | undefined) to a UI choice.
function toChoice(persisted: string | null | undefined): Choice {
  switch (persisted) {
    case 'quality':
    case 'cost':
    case 'latency':
      return persisted;
    default:
      return 'passthrough'; // null / absent / unknown → no default
  }
}

// Map a UI choice to the wire value (BR1-2 — passthrough clears to null).
function toValue(choice: Choice): RoutingStrategyValue {
  return choice === 'passthrough' ? null : choice;
}

interface RoutingStrategyFormProps {
  // The persisted default_routing_strategy from GET /v1/me (null/absent = none).
  defaultRoutingStrategy: string | null;
  etag: string;
  currentLocale: string;
}

export function RoutingStrategyForm({
  defaultRoutingStrategy,
  etag,
  currentLocale,
}: RoutingStrategyFormProps) {
  const t = useTranslations('account');
  const persisted = toChoice(defaultRoutingStrategy);
  const [selected, setSelected] = useState<Choice>(persisted);
  const [isPending, startTransition] = useTransition();
  // React 18 useTransition does NOT keep isPending across the awaited action
  // (pending spans only the synchronous part). An explicit isSaving flag gives
  // the real in-flight signal for the disable + double-submit guard (FLOW-002).
  const [isSaving, setIsSaving] = useState(false);
  const [result, setResult] = useState<UpdateRoutingStrategyResult | null>(null);

  const isDirty = selected !== persisted;
  const busy = isPending || isSaving;

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!isDirty || busy) return; // FLOW guard — no double submit
    setIsSaving(true);
    startTransition(async () => {
      try {
        const out = await updateMyRoutingStrategy({
          value: toValue(selected),
          ifMatch: etag,
          currentLocale,
        });
        setResult(out);
      } finally {
        setIsSaving(false);
      }
    });
  }

  return (
    <form aria-labelledby="routing-form-heading" className="space-y-6" onSubmit={onSubmit}>
      <header className="space-y-1">
        <h2 id="routing-form-heading" className="text-xl font-semibold">
          {t('routing.title')}
        </h2>
        <p className="text-sm text-neutral-500">{t('routing.description')}</p>
      </header>

      <fieldset
        role="radiogroup"
        aria-labelledby="routing-form-heading"
        disabled={busy}
        className="space-y-2"
      >
        {CHOICES.map((choice) => (
          <label key={choice} className="flex items-start gap-3 rounded border px-3 py-2">
            <input
              type="radio"
              name="default_routing_strategy"
              value={choice}
              checked={selected === choice}
              onChange={() => setSelected(choice)}
              className="mt-1"
            />
            <span>
              <span className="block text-sm font-medium">
                {t(`routing.options.${choice}` as never)}
              </span>
              <span className="block text-xs text-neutral-500">
                {t(`routing.hints.${choice}` as never)}
              </span>
            </span>
          </label>
        ))}
      </fieldset>

      {/* Concurrent-update banner */}
      {result?.kind === 'concurrent_update' && (
        <div role="alert" className="rounded border border-yellow-300 bg-yellow-50 p-3 text-sm">
          {t('routing.banners.concurrent_update.message')}{' '}
          <button type="button" onClick={() => location.reload()} className="font-medium underline">
            {t('routing.banners.concurrent_update.reload_cta')}
          </button>
        </div>
      )}

      {/* Rate limit */}
      {result?.kind === 'rate_limited' && (
        <div role="alert" className="rounded border border-orange-300 bg-orange-50 p-3 text-sm">
          {t('routing.errors.rate_limited')}
        </div>
      )}

      {/* Generic / validation / unauthorized errors */}
      {(result?.kind === 'error' ||
        result?.kind === 'validation' ||
        result?.kind === 'unauthorized') && (
        <div role="alert" className="rounded border border-red-300 bg-red-50 p-3 text-sm">
          {t('routing.errors.generic')}
        </div>
      )}

      {/* Success */}
      {result?.kind === 'ok' && (
        <div
          role="status"
          aria-live="polite"
          className="rounded border border-green-300 bg-green-50 p-3 text-sm"
        >
          {t('routing.toast.saved')}
        </div>
      )}

      <button
        type="submit"
        disabled={!isDirty || busy}
        className="rounded bg-neutral-900 px-4 py-2 text-sm font-medium text-white disabled:opacity-50"
      >
        {t('routing.actions.save')}
      </button>
    </form>
  );
}
