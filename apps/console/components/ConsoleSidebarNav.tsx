'use client';

/**
 * Story 5.5 T0.3 — (console) sidebar nav with active-state.
 *
 * Client island so the shared (console) layout can stay a Server Component
 * (auth gate) while the nav toggles `aria-current="page"` on the active route
 * (BR-L-9, parity with Story 4.7's marketing nav).
 */

import { usePathname } from 'next/navigation';

import { cn } from '@/lib/utils';

export interface ConsoleNavItem {
  href: string;
  label: string;
}

export function ConsoleSidebarNav({ items }: { items: ConsoleNavItem[] }) {
  const pathname = usePathname();
  return (
    <nav className="flex flex-col gap-1 text-sm">
      {items.map((item) => {
        const active = pathname === item.href || pathname?.startsWith(`${item.href}/`);
        return (
          <a
            key={item.href}
            href={item.href}
            aria-current={active ? 'page' : undefined}
            className={cn('rounded px-3 py-2 hover:bg-neutral-100', active && 'bg-neutral-100 font-medium')}
          >
            {item.label}
          </a>
        );
      })}
    </nav>
  );
}
