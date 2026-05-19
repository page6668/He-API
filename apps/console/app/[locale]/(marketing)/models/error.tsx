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

  return (
    <div
      role="alert"
      className="border border-red-300 bg-red-50 px-4 py-3 rounded text-red-900"
    >
      <p>{t("error.generic")}</p>
      <button
        type="button"
        onClick={reset}
        className="mt-2 underline text-sm"
      >
        Retry
      </button>
    </div>
  );
}
