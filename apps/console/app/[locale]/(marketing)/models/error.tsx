/**
 * Story 4.7 T3.3 — Next.js error boundary for `/{locale}/models`.
 *
 * Renders the BR-error-handling row 4 generic error UI with i18n key
 * `models.error.generic`. Sentry capture (Story-1.4 cascade) attaches
 * automatically via the Next.js error-boundary hook.
 */
"use client";

import { useEffect } from "react";
import { useTranslations } from "next-intl";

import { Notice } from "@/components/ui/kit";

interface ErrorBoundaryProps {
  error: Error & { digest?: string };
  reset: () => void;
}

export default function ModelsErrorBoundary({ error, reset }: ErrorBoundaryProps) {
  const t = useTranslations("models");

  useEffect(() => {
    // Sentry / OTEL captures via the browser-side hook in Story 1.4.
    console.error("[models/error] render exception:", error);
  }, [error]);

  // 错状态 = 一行安静说明 + 文字链重试(design-system:禁止满宽红底 banner)
  return (
    <div className="mx-auto max-w-prose-page px-6 py-10 lg:px-8">
      <Notice tone="error" role="alert">
        <p>{t("error.generic")}</p>
        <button
          type="button"
          onClick={reset}
          className="mt-2 rounded-sm text-small underline underline-offset-2 transition-colors duration-state ease-he hover:text-ink focus:outline-none focus:ring-2 focus:ring-crimson/25"
        >
          Retry
        </button>
      </Notice>
    </div>
  );
}
