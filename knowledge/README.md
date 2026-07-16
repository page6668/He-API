# knowledge/ — the project brain

**Audience: AI-primary, human-audited.** Skills read slices of this as context
(the `reads:` field). Humans do not browse it — they **seed, audit, and correct**
it. The metabolism loop (design-\* skills, human accept corrections) writes back
here via `updates:`.

## Format rules (these files are data, not docs)

1. **Structured, not prose.** Lists / entries / records — parseable by AI,
   diffable by a human auditor. No essays.
2. **Chunked for slice retrieval.** A skill reads only the slice it needs. Keep
   files small and single-topic so nobody loads the whole brain (that would
   re-introduce the context tax we removed).
3. **Provenance on every entry.** `source` (human | which skill), `added`
   (date), `approved_by`. The brain poisons every run if wrong — entries must be
   auditable: "why does the org believe this?"
4. **Append / supersede, don't silently rewrite.** Corrections add or supersede
   entries so the history of what the org learned stays visible.

## Governance

Writing here is a **high-authority, audited** action. AI writes back only through
the metabolism loop, and the human is the value anchor for the brain — exactly as
the accept gate is the value anchor for work products.

## Slices

- `taste/` — declarative taste: how things should be made (read by implement,
  review-code, design-ui).
- `architecture/` — system structure, conventions, and decisions (read by
  draft-story, design-architecture, implement).
- `registry/` — structured facts: the actual api / db / models (read by
  draft-story, implement; updated by implement as they are built).
