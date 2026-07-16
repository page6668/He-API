'use client';

/**
 * Story 5.5 AC3 — ConfigureKeyDrawer (T3.1).
 *
 * Right-side drawer (>=md) / full-screen (<md) that edits a key's model scope,
 * IP whitelist, and monthly cost cap. Pre-filled from the row's current state
 * (no separate GET — BR-U-1). Only changed fields are PATCHed (BR-U-2, diff via
 * native dirty comparison — the repo builds forms without react-hook-form). The
 * model catalogue is supplied by the server (BFF boundary: the gateway is
 * server-only) — an empty list disables the selector with a tooltip (BR-U-7).
 */

import { useMemo, useState, useTransition, type FormEvent, type RefObject } from 'react';
import { Loader2 } from 'lucide-react';
import { useTranslations } from 'next-intl';

import { Button, Notice } from '@/components/ui/kit';
import { Drawer, Dialog } from '@/components/ui/dialog';
import { parseDecimal, formatDecimal } from '@/lib/api/money';
import { validateIpRule } from '@/lib/api/ip';
import { readScope, type KeyConfigPatch, type KeyEntry } from '@/lib/api/me-keys';
import { updateMyKey } from '@/app/[locale]/(console)/keys/_actions/update-key';
import { IpWhitelistEditor } from './IpWhitelistEditor';

export interface ConfigureKeyDrawerProps {
  keyEntry: KeyEntry;
  locale: string;
  availableModels: string[];
  onClose: () => void;
  /** Panel-level toast. */
  notify: (variant: 'success' | 'error', messageKey: string) => void;
  /** Re-render the server list (router.refresh) after a mutation. */
  onMutated: () => void;
  triggerRef?: RefObject<HTMLElement | null>;
}

function arraysEqual(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

const IP_ERROR_KEY: Record<string, string> = {
  invalid_format: 'account.keys.errors.ip.invalid_format',
  degenerate_cidr: 'account.keys.errors.ip.degenerate_cidr',
  zone_id_rejected: 'account.keys.errors.ip.zone_id_rejected',
};

export function ConfigureKeyDrawer({
  keyEntry,
  locale,
  availableModels,
  onClose,
  notify,
  onMutated,
  triggerRef,
}: ConfigureKeyDrawerProps) {
  const t = useTranslations('account.keys');
  const tRoot = useTranslations();
  const [isPending, startTransition] = useTransition();

  const scope0 = useMemo(() => readScope(keyEntry.scope), [keyEntry.scope]);
  const cap0 = keyEntry.monthly_cost_cap_usd; // canonical string | null

  const [allModels, setAllModels] = useState(scope0.models.length === 0);
  const [models, setModels] = useState<string[]>(scope0.models);
  const [ipRows, setIpRows] = useState<string[]>(scope0.ip_whitelist);
  const [noCap, setNoCap] = useState(cap0 === null);
  const [capInput, setCapInput] = useState(cap0 === null ? '' : formatDecimal(cap0, locale));
  const [toast, setToast] = useState<string | null>(null);
  const [showDiscard, setShowDiscard] = useState(false);

  // ----- derived validation + diff -----
  const rowErrors = ipRows.map((r) => {
    const trimmed = r.trim();
    if (trimmed === '') return null;
    const verdict = validateIpRule(trimmed);
    return verdict.valid ? null : (IP_ERROR_KEY[verdict.error!] ?? 'account.keys.errors.ip.invalid_format');
  });
  const ipHasError = rowErrors.some((e) => e !== null);

  const capParse = noCap ? { value: null, error: null } : parseDecimal(capInput, locale);
  const capValue = capParse.value; // canonical | null
  const capError = capParse.error
    ? capParse.error === 'out_of_range'
      ? 'account.keys.errors.cap.out_of_range'
      : 'account.keys.errors.cap.invalid_format'
    : null;

  const modelsNow = allModels ? [] : models;
  const cleanedIps = ipRows.map((r) => r.trim()).filter((r) => r !== '');
  const modelsChanged = !arraysEqual(modelsNow, scope0.models);
  const ipChanged = !arraysEqual(cleanedIps, scope0.ip_whitelist);
  const capChanged = capValue !== cap0;
  const isDirty = modelsChanged || ipChanged || capChanged;
  const hasError = ipHasError || capError !== null;
  const saveDisabled = !isDirty || hasError || isPending;

  function buildPatch(): KeyConfigPatch {
    const patch: KeyConfigPatch = {};
    if (modelsChanged || ipChanged) {
      patch.scope = {};
      if (modelsChanged) patch.scope.models = modelsNow;
      if (ipChanged) patch.scope.ip_whitelist = cleanedIps;
    }
    if (capChanged) patch.monthly_cost_cap_usd = capValue;
    return patch;
  }

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (saveDisabled) return;
    setToast(null);
    const patch = buildPatch();
    startTransition(async () => {
      const res = await updateMyKey({ api_key_id: keyEntry.api_key_id, patch });
      if (res.ok) {
        notify('success', 'account.keys.edit.success');
        onMutated();
        onClose();
        return;
      }
      const code = res.error.code;
      if (code === 'account.keys.edit.errors.not_found') {
        notify('error', code);
        onMutated();
        onClose();
        return;
      }
      // invalid_request / generic / pending_deletion → keep the form (no
      // optimistic mutation; the user's input is retained — BR-DATA-003).
      setToast(tRoot(code));
    });
  }

  function toggleModel(id: string) {
    setModels((prev) => (prev.includes(id) ? prev.filter((m) => m !== id) : [...prev, id]));
  }

  function handleCloseAttempt() {
    if (isDirty) setShowDiscard(true);
    else onClose();
  }

  return (
    <Drawer titleId="configure-key-title" onClose={handleCloseAttempt} restoreFocusTo={triggerRef}>
      <form onSubmit={onSubmit} className="space-y-6">
        <h2 id="configure-key-title" className="text-h3 text-ink">
          {t('edit.title')}
        </h2>

        {/* Models scope */}
        <section className="space-y-2">
          <h3 className="text-small font-medium text-ink">{t('configure.models.heading')}</h3>
          <label className="flex items-center gap-2 text-small text-ink">
            <input
              type="checkbox"
              checked={allModels}
              onChange={(e) => setAllModels(e.target.checked)}
              className="h-4 w-4 rounded-sm border-line-strong text-ink accent-ink"
            />
            {t('configure.models.all_toggle')}
          </label>
          {!allModels &&
            (availableModels.length === 0 ? (
              <p className="text-small text-ink-muted" title={t('configure.models.unavailable')}>
                {t('configure.models.unavailable')}
              </p>
            ) : (
              <fieldset className="space-y-1">
                <legend className="sr-only">{t('configure.models.select_label')}</legend>
                {availableModels.map((id) => (
                  <label key={id} className="flex items-center gap-2 text-small text-ink">
                    <input
                      type="checkbox"
                      checked={models.includes(id)}
                      onChange={() => toggleModel(id)}
                      className="h-4 w-4 rounded-sm border-line-strong text-ink accent-ink"
                    />
                    <span className="tabular">{id}</span>
                  </label>
                ))}
              </fieldset>
            ))}
        </section>

        {/* IP whitelist */}
        <section className="space-y-2">
          <h3 className="text-small font-medium text-ink">{t('configure.ip.heading')}</h3>
          <IpWhitelistEditor rows={ipRows} rowErrors={rowErrors} onChange={setIpRows} />
        </section>

        {/* Monthly cost cap */}
        <section className="space-y-2">
          <h3 className="text-small font-medium text-ink">{t('configure.cap.heading')}</h3>
          <label className="flex items-center gap-2 text-small text-ink">
            <input
              type="checkbox"
              checked={noCap}
              onChange={(e) => {
                setNoCap(e.target.checked);
                if (e.target.checked) setCapInput('');
              }}
              className="h-4 w-4 rounded-sm border-line-strong text-ink accent-ink"
            />
            {t('configure.cap.no_cap')}
          </label>
          <input
            type="text"
            inputMode="decimal"
            dir="ltr"
            disabled={noCap}
            value={capInput}
            onChange={(e) => setCapInput(e.target.value)}
            aria-label={t('configure.cap.label')}
            aria-describedby="cap-help"
            aria-invalid={capError ? true : undefined}
            className="tabular w-40 rounded-md border border-line-strong bg-surface px-3 py-1.5 text-small text-ink outline-none transition-colors duration-state ease-he focus:border-seal focus:ring-2 focus:ring-seal/15 disabled:cursor-not-allowed disabled:opacity-50"
          />
          <p id="cap-help" className="text-label text-ink-muted">
            {t('configure.cap.help')}
          </p>
          {capError && (
            <p role="alert" className="text-label text-crimson">
              {tRoot(capError)}
            </p>
          )}
        </section>

        {toast && (
          <Notice tone="error" role="alert">
            {toast}
          </Notice>
        )}

        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={handleCloseAttempt} disabled={isPending}>
            {t('configure.cancel')}
          </Button>
          {/* 本屏唯一的朱砂 —— 主操作(design-system.md distinctive_rule 铁律2) */}
          <Button type="submit" variant="primary" disabled={saveDisabled}>
            {isPending && <Loader2 className="me-1.5 h-4 w-4 animate-spin" aria-hidden="true" />}
            {t('edit.confirm.cta')}
          </Button>
        </div>
      </form>

      {showDiscard && (
        <Dialog
          titleId="discard-title"
          role="alertdialog"
          closeOnOverlay={false}
          onClose={() => setShowDiscard(false)}
          className="rounded-lg border border-line bg-surface shadow-overlay"
        >
          <h2 id="discard-title" className="text-h3 text-ink">
            {t('configure.discard.title')}
          </h2>
          <p className="mt-2 text-small text-ink-secondary">{t('configure.discard.body')}</p>
          <div className="mt-4 flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setShowDiscard(false)}>
              {t('configure.discard.cancel')}
            </Button>
            <Button type="button" variant="danger" onClick={onClose}>
              {t('configure.discard.confirm')}
            </Button>
          </div>
        </Dialog>
      )}
    </Drawer>
  );
}
