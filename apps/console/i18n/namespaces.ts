/**
 * Story 10.1 (OQ-10.1-1 ratified) — the single source of truth for the console's
 * i18n namespaces. This ordered list MUST equal the set of `messages/en/*.json`
 * basenames; a unit test (10.1-INT-002) asserts that parity so a newly-added
 * namespace can never be silently dropped from the runtime wiring.
 *
 * Consumed by `i18n/request.ts` (loads every namespace per locale). The key-set /
 * codegen scripts discover namespaces from the en directory directly (the files
 * are the de-facto SoT), and the test ties this array to that directory — so there
 * is no third, drift-prone hand-maintained list.
 */
export const NAMESPACES = ['account', 'auth', 'benchmark', 'common', 'dashboard', 'logs', 'models', 'playground'] as const;

export type Namespace = (typeof NAMESPACES)[number];
