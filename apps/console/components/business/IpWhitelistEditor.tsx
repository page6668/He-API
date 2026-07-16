'use client';

/**
 * Story 5.5 AC3 — IpWhitelistEditor (T3.2).
 *
 * Editable list of IP / CIDR rows. The parent (ConfigureKeyDrawer) owns the
 * `rows` array and validation verdicts; this component renders the inputs,
 * add/remove controls, and per-row errors. IP literals render `dir="ltr"` even
 * under RTL (Q-RTL1). Empty rows are kept while editing and dropped on submit
 * by the parent (BR-U-4).
 */

import { useEffect, useRef, useState } from 'react';
import { X, Plus } from 'lucide-react';
import { useTranslations } from 'next-intl';

export interface IpWhitelistEditorProps {
  rows: string[];
  /** Per-row error i18n key (or null). Same length as `rows`. */
  rowErrors: (string | null)[];
  onChange: (rows: string[]) => void;
}

export function IpWhitelistEditor({ rows, rowErrors, onChange }: IpWhitelistEditorProps) {
  const t = useTranslations('account.keys');
  const tRoot = useTranslations();
  const inputsRef = useRef<(HTMLInputElement | null)[]>([]);
  const [focusIndex, setFocusIndex] = useState<number | null>(null);

  useEffect(() => {
    if (focusIndex === null) return;
    inputsRef.current[focusIndex]?.focus();
    setFocusIndex(null);
  }, [focusIndex]);

  function setRow(index: number, value: string) {
    onChange(rows.map((r, i) => (i === index ? value : r)));
  }

  function addRow() {
    onChange([...rows, '']);
    setFocusIndex(rows.length);
  }

  function removeRow(index: number) {
    onChange(rows.filter((_, i) => i !== index));
    setFocusIndex(Math.max(0, index - 1));
  }

  return (
    <div className="space-y-2">
      {rows.length === 0 && (
        <p className="text-small text-ink-muted">{t('configure.ip.empty')}</p>
      )}
      {rows.map((row, index) => {
        const error = rowErrors[index];
        return (
          <div key={index} className="space-y-1">
            <div className="flex items-center gap-2">
              <label htmlFor={`ip-${index}`} className="sr-only">
                {t('configure.ip.row_label', { index: index + 1 })}
              </label>
              <input
                id={`ip-${index}`}
                ref={(el) => {
                  inputsRef.current[index] = el;
                }}
                type="text"
                dir="ltr"
                value={row}
                onChange={(e) => setRow(index, e.target.value)}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `ip-${index}-error` : undefined}
                className="tabular w-full rounded-md border border-line-strong bg-surface px-3 py-1.5 text-small text-ink outline-none transition-colors duration-state ease-he focus:border-seal focus:ring-2 focus:ring-seal/15"
              />
              <button
                type="button"
                onClick={() => removeRow(index)}
                aria-label={t('configure.ip.remove', { value: row })}
                className="rounded-md p-1 text-ink-muted transition-colors duration-state ease-he hover:bg-surface-sunken hover:text-ink"
              >
                <X className="h-4 w-4" aria-hidden="true" />
              </button>
            </div>
            {error && (
              <p id={`ip-${index}-error`} role="alert" className="text-label text-crimson">
                {tRoot(error)}
              </p>
            )}
          </div>
        );
      })}
      <button
        type="button"
        onClick={addRow}
        className="inline-flex items-center gap-1 text-small text-ink-secondary transition-colors duration-state ease-he hover:text-ink"
      >
        <Plus className="h-4 w-4" aria-hidden="true" />
        {rows.length === 0 ? t('configure.ip.add_first') : t('configure.ip.add')}
      </button>
    </div>
  );
}
