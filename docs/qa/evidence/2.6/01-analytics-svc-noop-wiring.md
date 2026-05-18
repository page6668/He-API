# Evidence 01 — analytics-svc production binary wires NoOp implementations

**Severity:** CRITICAL
**ACs affected:** AC4 (full), AC5 (email trigger), AC6 (completed/failed audit emit)
**Story claim countered:** "All 6 ACs land with explicit scope-bound deferrals" (Change Log 2026-05-18 Dev entry)

## Reproduction

```bash
$ grep -n "noopStore\|noopUploader\|noopEmail\|noopAudit\|emptyDumper" \
    apps/analytics-svc/cmd/server/main.go
```

Key wirings (`apps/analytics-svc/cmd/server/main.go`):

| Line | Wiring |
|------|--------|
| 105 | `store := &noopStore{}` |
| 106 | `uploader := &noopUploader{}` |
| 107 | `email := &noopEmail{}` |
| 108 | `audit := &noopAudit{logger: logger}` |
| 110-116 | `dumpers` filled with `&emptyDumper{name: ...}` × 7 |

NoOp behavior (lines 148-199):

- `noopStore.MarkProcessing` returns `(false, nil)` → BR-4.2 claim path ALWAYS skips. Every Kafka message is ACK'd without work.
- `noopUploader.PutObject` returns `nil` (no upload). `SignURL` returns the literal string `"https://placeholder/<key>"` — not a real signed URL.
- `noopEmail.Send` returns `nil` (no email sent).
- `noopAudit.EmitCompleted/EmitFailed` writes a stdout log line — no Kafka publish.
- `emptyDumper.Dump` writes the literal `[]` for every table → all 7 ZIP files are empty JSON arrays.

## User-visible impact

1. User requests an export via console → AC1 + AC2 paths work → row inserted, Kafka `gdpr.export.requested` produced, audit `gdpr.export.requested` emitted.
2. analytics-svc consumes the message → `MarkProcessing` returns false → log "already claimed; ack and skip" → ACK without work.
3. The PG row stays at `status='pending'` indefinitely (24h+ idempotency window).
4. User refreshes Settings → Data → CTA stays disabled → tooltip "An export is being prepared. We'll email you…" — forever.
5. No email is ever sent. No data ever leaves PG / CH. No audit `gdpr.export.completed` or `gdpr.export.failed` is emitted.

## Dev's own acknowledgement

`docs/dev/logs/2.6-dev-log.md` line 24:
> "Aliyun OSS SDK (`alicloud-sdk-go`) + ClickHouse Go client — NOT yet vendored to any go.mod. analytics-svc boots with NoOp implementations of the Uploader / Store / EmailTrigger / Audit interfaces"

The deferral is honest, but the cumulative effect is that **the user-visible deliverable of Story 2.6 (downloadable data export via email) is non-functional in the production binary.**

## Why this is QA-blocking, not just a deferred task

- Stories 2.2 / 2.3 / 2.4 / 2.5 delivered end-to-end working features at QA. Story 2.6's pattern of "ship interfaces; defer implementations" inverts the contract.
- AC4 / AC5 are not "minor optimizations" — they are the user-facing value of the entire story. AC1+AC2 alone are a 404 to the user: "click button → row in DB → silence forever".
- The test-design doc (Comprehensive level, 142 scenarios) explicitly scopes AC4/AC5 as P0 paths; deferring them is a scope reduction that should have prompted SM review.

## Recommended remediation

Block the merge until at least one of:
1. **Option A**: Vendor the OSS + CH Go SDKs and wire real implementations (full AC4/AC5 delivery).
2. **Option B**: Split Story 2.6 into 2.6a (request path, ACs 1/2/3/6-partial — what's done) and 2.6b (worker path, ACs 4/5/6-completed/failed). Mark 2.6a as Done; 2.6b as a new Story.
3. **Option C**: Add a startup-time feature flag that PREVENTS analytics-svc from subscribing to `gdpr.export.requested` while the NoOp implementations are wired. This avoids the failure mode where requests pile up at `pending`.

Option C is the lowest-cost fix; Option B is the cleanest scope reset.
