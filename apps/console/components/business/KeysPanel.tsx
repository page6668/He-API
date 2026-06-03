'use client';

/**
 * Story 5.5 — KeysPanel.
 *
 * Client orchestrator for the keys page: owns the modal/drawer/dialog open
 * state, the page-level toast, and the post-mutation refresh. State coherence
 * after a mutation is `router.refresh()` (Q-T1 — Server Components +
 * router.refresh in lieu of TanStack Query / client cache). The Server
 * Component (page.tsx) renders the <h1> chrome and passes the SSR-fetched keys
 * + model catalogue here.
 */

import { useRef, useState } from 'react';
import { useRouter } from 'next/navigation';
import { Plus } from 'lucide-react';
import { useTranslations } from 'next-intl';

import { Button } from '@/components/ui/button';
import { Toast, type ToastVariant } from '@/components/ui/toast';
import type { KeyEntry } from '@/lib/api/me-keys';
import { KeysTable } from './KeysTable';
import { KeysEmptyState } from './KeysEmptyState';
import { CreateKeyModal } from './CreateKeyModal';
import { ConfigureKeyDrawer } from './ConfigureKeyDrawer';
import { RevokeKeyDialog } from './RevokeKeyDialog';

export interface KeysPanelProps {
  keys: KeyEntry[];
  locale: string;
  availableModels: string[];
}

type OpenState =
  | { kind: 'create' }
  | { kind: 'configure'; key: KeyEntry }
  | { kind: 'revoke'; key: KeyEntry }
  | null;

export function KeysPanel({ keys, locale, availableModels }: KeysPanelProps) {
  const t = useTranslations('account.keys');
  const tRoot = useTranslations();
  const router = useRouter();
  const [open, setOpen] = useState<OpenState>(null);
  const [toast, setToast] = useState<{ variant: ToastVariant; key: string } | null>(null);
  const newKeyRef = useRef<HTMLButtonElement | null>(null);

  const close = () => setOpen(null);
  const notify = (variant: ToastVariant, key: string) => setToast({ variant, key });
  const onMutated = () => router.refresh();

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-end">
        <Button ref={newKeyRef} onClick={() => setOpen({ kind: 'create' })}>
          <Plus className="mr-1.5 h-4 w-4" aria-hidden="true" />
          {t('page.new_key')}
        </Button>
      </div>

      {keys.length === 0 ? (
        <KeysEmptyState onCreate={() => setOpen({ kind: 'create' })} />
      ) : (
        <>
          <KeysTable
            keys={keys}
            locale={locale}
            onConfigure={(key) => setOpen({ kind: 'configure', key })}
            onRevoke={(key) => setOpen({ kind: 'revoke', key })}
          />
          <p className="text-xs text-neutral-500">{t('footer.count', { count: keys.length })}</p>
        </>
      )}

      {open?.kind === 'create' && (
        <CreateKeyModal locale={locale} onClose={close} triggerRef={newKeyRef} />
      )}
      {open?.kind === 'configure' && (
        <ConfigureKeyDrawer
          keyEntry={open.key}
          locale={locale}
          availableModels={availableModels}
          onClose={close}
          notify={notify}
          onMutated={onMutated}
        />
      )}
      {open?.kind === 'revoke' && (
        <RevokeKeyDialog
          keyEntry={open.key}
          onClose={close}
          notify={notify}
          onMutated={onMutated}
        />
      )}

      {toast && (
        <Toast variant={toast.variant} onDismiss={() => setToast(null)}>
          {tRoot(toast.key)}
        </Toast>
      )}
    </div>
  );
}
