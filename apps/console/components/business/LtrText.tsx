import type { ReactNode } from 'react';

/**
 * Story 10.1 (BR-10.1.8) — an LTR "island" for bidi-neutral data that must NOT be
 * reordered under an RTL (ar) document: timestamps, Request IDs, API keys, numbers,
 * benchmark/latency cells, and code/format strings (JSON·CSV). Wrapping the value
 * in `dir="ltr"` keeps e.g. `req_abc123` / `12:34` left-to-right even inside an
 * Arabic page, where the surrounding flow is right-to-left.
 *
 * Render as an inline `<span>` by default; pass `as="td"`/`as="div"` where the
 * island IS the cell/block so no extra wrapper element is introduced.
 */
export interface LtrTextProps {
  children: ReactNode;
  className?: string;
  as?: 'span' | 'div' | 'td';
}

export function LtrText({ children, className, as: Tag = 'span' }: LtrTextProps) {
  return (
    <Tag dir="ltr" className={className}>
      {children}
    </Tag>
  );
}
