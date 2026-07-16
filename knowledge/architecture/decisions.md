# architecture/decisions

ADR-style entries that `draft-story`, `design-architecture`, and `implement`
read. One entry per decision, terse, with provenance. Append-only; supersede,
do not rewrite.

## AD-001: Capability-first orchestration

- decision: Organize by capability (skills) wired by an orchestrator, not by role agents with handoffs.
- context: Role-handoff pipelines cost 30–50x in cold-start re-reads; strong models make them unnecessary.
- alternatives: BMAD-style role agents (rejected — handoff tax); single monolithic agent (rejected — no isolation/verification).
- status: accepted
- source: human
- added: 2026-06-27
- approved_by: dorayo
- ref: cc-plans/Skill编排器与最小契约设计-v0.1.md

<!--
Entry shape (copy for new decisions):

## AD-NNN: <title>
- decision: <one line>
- context: <why it came up, one line>
- alternatives: <what was rejected, one line>
- status: accepted | superseded by AD-MMM
- source: design-architecture | human
- added: <date>
- approved_by: <who>

Metabolism: design-architecture appends new ADRs here; a superseding decision
sets the old one's status to "superseded by AD-MMM" rather than deleting it.
-->
