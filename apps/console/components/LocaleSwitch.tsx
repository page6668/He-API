'use client';

import { useState, useTransition } from 'react';
import { usePathname } from 'next/navigation';
import { useTranslations } from 'next-intl';
import { ChevronDown, Loader2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { locales, type Locale } from '@/i18n/config';
import { setLocale } from '@/app/[locale]/_actions/locale';

interface LocaleSwitchProps {
  currentLocale: Locale;
}

export function LocaleSwitch({ currentLocale }: LocaleSwitchProps) {
  const t = useTranslations('common');
  const pathname = usePathname() ?? '/';
  const [open, setOpen] = useState(false);
  const [isPending, startTransition] = useTransition();

  const currentLabel = t(`localeSwitch.options.${currentLocale}` as const);

  function handleSelect(next: Locale) {
    if (next === currentLocale) return;
    startTransition(() => {
      void setLocale(next, currentLocale, pathname);
    });
  }

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          aria-label={t('localeSwitch.label')}
          disabled={isPending}
        >
          {isPending ? <Loader2 className="me-2 h-4 w-4 animate-spin" /> : null}
          <span>{currentLabel}</span>
          <ChevronDown className="ms-2 h-4 w-4" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {locales.map((loc) => {
          const isCurrent = loc === currentLocale;
          return (
            <DropdownMenuItem
              key={loc}
              onSelect={(e) => {
                if (isCurrent) {
                  e.preventDefault();
                  return;
                }
                handleSelect(loc);
              }}
              aria-current={isCurrent ? 'true' : undefined}
              aria-label={t(`localeSwitch.options.${loc}` as const)}
              data-locale={loc}
            >
              {t(`localeSwitch.options.${loc}` as const)}
            </DropdownMenuItem>
          );
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
