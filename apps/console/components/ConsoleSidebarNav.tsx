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
    <nav className="flex flex-col gap-1">
      {items.map((item) => {
        const active = pathname === item.href || pathname?.startsWith(`${item.href}/`);
        return (
          <a
            key={item.href}
            href={item.href}
            aria-current={active ? 'page' : undefined}
            // 激活态用墨色(bg-ink text-paper),不用朱砂 —— 朱砂每屏只落在主操作上。
            className={cn(
              'rounded-md px-3 py-2 text-small transition-colors duration-state ease-he focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-seal/30',
              active
                ? 'bg-ink font-medium text-paper'
                : 'text-ink-secondary hover:bg-surface-sunken hover:text-ink',
            )}
          >
            {item.label}
          </a>
        );
      })}
    </nav>
  );
}
