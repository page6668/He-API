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
import { Button, Notice, Panel } from '@/components/ui/kit';

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
      <Panel className="space-y-4">
        <header className="space-y-1">
          <h2 id="routing-form-heading" className="text-h2 text-ink">
            {t('routing.title')}
          </h2>
          <p className="text-small text-ink-secondary">{t('routing.description')}</p>
        </header>

        <fieldset
          role="radiogroup"
          aria-labelledby="routing-form-heading"
          disabled={busy}
          className="space-y-2"
        >
          {CHOICES.map((choice) => {
            const isSelected = selected === choice;
            return (
              // 选中态用墨色而非朱砂 —— 本页主操作(ProfileForm 的 Save)已占用唯一朱砂
              // (design-system.md distinctive_rule 铁律2)。
              <label
                key={choice}
                className={`flex items-start gap-3 rounded-lg border px-4 py-3 transition-colors duration-state ease-he ${
                  isSelected ? 'border-ink bg-surface-sunken' : 'border-line hover:border-line-strong'
                }`}
              >
                <input
                  type="radio"
                  name="default_routing_strategy"
                  value={choice}
                  checked={isSelected}
                  onChange={() => setSelected(choice)}
                  className="mt-1 h-4 w-4 border-line-strong text-ink accent-ink focus:outline-none focus:ring-2 focus:ring-ink/15"
                />
                <span>
                  <span className="block text-small font-medium text-ink">
                    {t(`routing.options.${choice}` as never)}
                  </span>
                  <span className="block text-label text-ink-muted">
                    {t(`routing.hints.${choice}` as never)}
                  </span>
                </span>
              </label>
            );
          })}
        </fieldset>
      </Panel>

      {/* Concurrent-update banner */}
      {result?.kind === 'concurrent_update' && (
        <Notice tone="warning" role="alert">
          {t('routing.banners.concurrent_update.message')}{' '}
          <button type="button" onClick={() => location.reload()} className="font-medium underline underline-offset-2">
            {t('routing.banners.concurrent_update.reload_cta')}
          </button>
        </Notice>
      )}

      {/* Rate limit */}
      {result?.kind === 'rate_limited' && (
        <Notice tone="warning" role="alert">
          {t('routing.errors.rate_limited')}
        </Notice>
      )}

      {/* Generic / validation / unauthorized errors */}
      {(result?.kind === 'error' ||
        result?.kind === 'validation' ||
        result?.kind === 'unauthorized') && (
        <Notice tone="error" role="alert">
          {t('routing.errors.generic')}
        </Notice>
      )}

      {/* Success — jade text, tone=neutral (design-system.md key_page_direction). */}
      {result?.kind === 'ok' && (
        <div
          role="status"
          aria-live="polite"
          className="rounded-lg border border-line bg-surface px-4 py-2.5 text-small text-jade"
        >
          {t('routing.toast.saved')}
        </div>
      )}

      {/* secondary —— 本页唯一朱砂已给 ProfileForm 的 Save(铁律2:每屏只允许一处朱砂）。 */}
      <Button type="submit" variant="secondary" disabled={!isDirty || busy}>
        {t('routing.actions.save')}
      </Button>
    </form>
  );
}
