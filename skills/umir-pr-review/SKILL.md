---
name: umir-pr-review
description: Use when reviewing UMIR gateway, frontend, shared HTTP contracts, service mocks, or Figma handoff pull requests. A spelling-only documentation edit does not require this project review context.
---

# UMIR integration PR review

This is portable project context for reviewing the second developer's work. It adds UMIR boundaries and evidence criteria; it does not duplicate a general review framework. Use the installed general `review` skill when available. Without it, the linked project criteria remain usable by a human or agent reviewer.

## Read the relevant contract

- For gateway, API and mock changes, read [the handoff specification](../../docs/gateway-frontend-handoff.md), especially sections 2–4 and 6–7.
- For proposal, order or quantity semantics, read the affected sections of [business-core](../../docs/business-core.md). It is authoritative for business behavior.
- For frontend/Figma work, read [the design brief](../../docs/frontend-design-brief.md) and inspect the actual changed screens, states and prototype.
- Read [integration criteria](references/integration.md) for the affected boundary. Do not turn a scoped PR into an unrelated repository audit.

## UMIR boundaries

Gateway consumes injected HTTP handlers/transports and forwards service results. FEFO, command fingerprints, proposal coordination and late-write prevention belong to business services. Mocks can demonstrate transport and UI behavior; real backend recovery remains a separate acceptance milestone.

In confirmation review, follow one unresolved attempt across commandID, proposalID and the immutable request. A 202, timeout, disconnected response, malformed payload or one missing lookup is not a recorded sale or proof of no sale. A reduced replacement needs a new explicit confirmation. Preserve integer strings beyond JavaScript's safe integer range.

## Evidence for this project

Identify the actual PR/base commit and API revision. For visual work identify Figma nodes and inspected exports/revision, prototype states, viewport and keyboard coverage. Build success, frame metadata and a link alone do not establish visual completion. Report only evidenced defects; unavailable renders leave visual coverage incomplete, not failed aesthetics.

Use capable reviewers for the material being checked. This project has no mandatory Luna model or fixed model family. Respect the user's runtime budget and model choices; complex protocol questions need an independent technical check, while visual claims need actual image access. Do not silently substitute inaccessible evidence.

Review does not authorize publishing comments, changing code, creating issues, merging PRs or updating image pins. Perform those actions only when the active request authorizes them. The project owner performs the merge through the existing repository rules.
