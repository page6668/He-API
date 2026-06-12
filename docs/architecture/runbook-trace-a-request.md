# Runbook — Trace one request by `he.request_id` (Story 9.4)

**Audience:** on-call / support engineers.
**Goal:** given a user-reported request, see its full sync + async waterfall in Grafana and pivot between trace and logs.

This runbook is the actionable form of the §11.5 promise: `he.request_id` is the
customer-support correlation handle. Story 9.4 made the underlying trace
*continuous* end-to-end (one `trace_id` across gateway → routing → auth → adapter
→ upstream model, plus the async Kafka legs via span links), so the handle now
resolves to ONE connected trace.

---

## Key idea

- `he.request_id` (`req_<12 hex>`) is what the **operator types** — it is on every
  span and every slog line of the request.
- `trace_id` (32 hex) is what the **tools join on**. `he.request_id` is
  deterministically derived from it: `he_request_id = "req_" + hex(trace_id[0:6])`.
  So once you have either, you have the trace.

---

## A. Start from a `he.request_id` (the usual support path)

A ticket cites e.g. `he.request_id=req_a1b2c3d4e5f6`.

1. **Grafana → Explore → Jaeger** datasource.
2. Search by tag: `he.request_id=req_a1b2c3d4e5f6` (Jaeger indexes `he.*` span
   tags). Pick the matching trace.
3. Read the **waterfall**:
   - root = the gateway server span (`POST /v1/chat/completions`, …);
   - children = `gateway→routing-svc`, `routing-svc` server, `gateway→adapter`
     client, and the **`adapter→<vendor>` upstream-model client span** — the
     business-critical latency (TTFB to the model, BR-TR-6);
   - **async legs** (billing credit-apply, analytics request_logs ingest,
     usage-log export) appear as **linked** spans (Q-KAFKA: span LINK, not child) —
     follow the link to the consumer trace.
4. **Pivot to logs**: click any span → use the Jaeger datasource
   **`tracesToLogsV2`** link (9.4 BR-TR-14) → Grafana opens Loki filtered to that
   `trace_id`. Every slog line for the request is there.

## B. Start from a log line (the reverse path)

You found an error log in Loki first.

1. In the Loki log line, the JSON carries `"trace_id":"…"` (and `he_request_id`).
2. Click the **`trace_id` derived-field** link (Loki→Jaeger, shipped by 1.4/3.6) →
   Grafana opens the trace in Jaeger. Proceed as in A.3.

---

## What you will NOT see (by design — security)

Spans and Kafka headers are a strict allow-list (BR-TR-7): registered `he.*` +
OTel `http.*`/`rpc.*`/`db.system` semconv ONLY. You will **never** see the chat
prompt/response, `Authorization`/`X-He-Api-Key`, api-key plaintext/hash, user
email, or client IP in a trace. A collector-side `redaction` keep-list drops any
stray attribute before Jaeger storage as defense-in-depth — so even an operator
error can't leak PII into the store. If you need the request *content*, that is a
separate, access-controlled path — it is intentionally not in tracing.

## Gotchas

- **"Trace not found"**: Jaeger storage is in-memory in staging (~2h eviction,
  1.4 Q4). If the trace aged out, fall back to logs-only (path B gives you the
  `he.request_id`/`trace_id` from any surviving log line).
- **Request is the trace root with no parent**: correct for the true entry edge
  (or a malformed incoming `traceparent` → otelhttp roots a new span — BOUNDARY-001).
- **A header-less Kafka message** (produced by a pre-9.4 build) roots its own
  trace with no link — back-compat, not a bug.
- **Sampling**: prod head ratio is `0.1` (infrastructure-deployment §7.6) — a given
  request may simply not have been sampled. Errors are retained at 100% by the
  collector tail sampler. Sampling drops *volume*, never *masks PII*.
