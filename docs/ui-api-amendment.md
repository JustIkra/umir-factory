# UI API amendment UA1

Status: proposed for approval. No implementation or runtime acceptance is claimed.
Date: 2026-10-14. Time zone: Europe/Moscow.

UA1 supplies product batches, organization contacts, invitation lists and invitation
link delivery to the existing frontend. Factory owns the contract. Warehouse owns
batch reads. Auth owns organization reads and durable invitation mail. Gateway
publishes the routes and validates both boundaries. The frontend developer owns
the invitation page and the use of these results.

The binding predecessor is Factory commit
`deddf0a789ebf5ed801cb7ec0e92cbac82c3297a`, including
[backend-next-steps](backend-next-steps.md), [public API](api/backend-v2-public.json),
[private API](api/backend-v2-private.json) and [business rules](business-core.md).
UA1 is a separately named contract profile. Its proposed wire additions are in
[public UA1](api/ui-amendment-v1-public.json) and
[private UA1](api/ui-amendment-v1-private.json).
Unlisted operations keep their predecessor contract. Approval of this document
does not select production SHAs, merge a branch, publish a release or deploy.

## Proposed decisions

These choices are proposed by the implementer and require approval with UA1.
Approval covers every normative value and rule in this document and its linked
wire/acceptance artifacts. It does not claim that those rules already run.

| Choice | Proposed behavior |
| --- | --- |
| Batch scope | All positive remaining batches for one product, including pending writeoff. Consumed and written-off zero balances are omitted. |
| Pagination | Limit 100 by default, maximum 500. Opaque cursors retain one immutable snapshot for five physical minutes. |
| Missing legacy email | Required `email: null`. An address is returned only from the verified User identity. |
| Member list | Active and revoked memberships. Default state filter is `active`. |
| Invitation list | All five existing invitation states by default. A single optional state filter is supported. |
| Link resend | Add `POST .../resend-link`. Keep existing `POST .../resend` as OTP resend. |
| Link creation | A successful create atomically saves exactly one link-mail job with the invitation and original command result. The 201 Invitation response shape stays unchanged. |
| Link URL | Configured trusted frontend origin plus `/invite/{invitationID}`. The URL contains no email, OTP, proof, session or service credential. |
| Link lifetime | Keep the original seven-day invitation deadline. Resend does not extend it. |
| Cooldown and rate bounds | Share the existing 60-second invitation cooldown with OTP resend. Add at most five fresh link resends per invitation per rolling hour; keep three creates per org/email per rolling ten minutes. |
| Mail issue limits | Link acceptance joins the existing limit of three mail issues per comparison-email target and 60 globally per rolling ten minutes. Counting links reduces the OTP issues available inside that shared window. |
| Link delivery retries | Same job/Message-ID, at most three attempts, delays 1 then 5 minutes, matching the existing OTP worker. Exhausted ambiguous link delivery remains unknown. OTP retry timing stays unchanged. |
| Snapshot capacity | 128 active captures per Auth organization or Warehouse restaurant, plus finite implementation bounds on rows/bytes. |
| Storage upgrade | New validated Auth format v2; explicit migration and legacy command replay, no automatic link-mail backfill. |

The existing `resendInvitation` source requires a live challenge and resends its
OTP. Replacing it with link resend would break an existing operation. The separate
`resend-link` operation also works before a challenge exists.

## Shared request and read rules

All three collection GETs require a real session through Gateway. Query names,
duplicate scalar values, limits and identifiers are validated before owner dispatch.
Unknown query names, duplicate values and bodies on GET return 400 InvalidArgument.
`limit` is an integer from 1 to 500. `cursor` is an opaque unguessable string of
1 to 128 characters. A continuation repeats the same limit and filters as page one.
Unknown, expired or mismatched cursors return 409 RefreshRequired with
`details.refreshRequired=true`; they never start a fresh list implicitly.
End pages omit `nextCursor`. They do not return null or an empty cursor.

Capture filtered rows, ordering, time and identity together. A cursor binds the
current actor, organization, restaurant when applicable, reader/runtime epoch,
captured revision/state version, normalized filters, limit and next index.
Mutations after capture do not change existing pages. Every page requires a fresh
authorization decision. Revocation, session expiry and scope deletion deny further
pages. A cursor does not grant access. Cross-scope requests fail authorization
before any snapshot bytes are returned. An authorized actor using another actor's
cursor gets RefreshRequired without disclosing its owner.

Retain at most 128 active snapshots per organization for Auth, and per restaurant
for Warehouse. Remove expired snapshots using bounded physical timers. Exhausted
capacity returns 503 LimitExceeded. Allocation limits also apply to copied rows
and total bytes; the implementation must choose and document finite bounds before
acceptance. Exceeding a bound returns an explicit error without truncation.
Restart discards cursors. No paging store is added to auth.json.

## Product batches

Public: `GET /api/v2/r/{restaurantID}/warehouse/products/{productID}/batches`.
Private: `GET /internal/warehouse/v2/r/{restaurantID}/products/{productID}/batches`.
Capability: `stock.read` for the selected restaurant. Gateway obtains the existing
fresh scoped service JWT. The owner validates org, restaurant, epoch and settings.
No new role or capability is introduced.

Allowed query: `limit`, `cursor`. Product IDs keep the existing opaque ID semantics.
An encoded slash, control character or unsupported path representation is rejected
by the same Gateway/owner path policy; it is never silently renamed. A product that
exists with no positive batches returns 200 with an empty list. An unknown product
returns 404 NotFound after scope authorization.

The first page uses Warehouse's ordinary atomic expiry preparation. Due batches
become pending writeoff through the existing owner mechanism. It does not confirm
writeoff. Emit the existing invalidation signal when preparation commits transitions.
Capture the resulting state and return all positive batches for that product.
The context carries the current organization settings. Each batch retains its own
original expiry, applied settings and tzdataVersion.

`result.productID` and every batch productID equal the route productID.
`context.tenantID` equals restaurantID. `context.runtimeEpoch` and
`result.page.epoch` are the same Warehouse epoch. Context and page stateVersion,
evaluatedAt and current settings describe one capture. Page prefixSequence is the
same captured committed event prefix. Repeat all capture metadata on every page.

Rows contain a complete existing BatchV2, its baseUnit, and `available` or
`pending_writeoff`. Remaining quantity is strictly positive and stays a decimal
string in thousandths of g/ml/pcs. Sort by expiresAt instant, receivedAt instant,
then batchID in UTF-8 byte order. Apply this FEFO order before slicing pages.
Do not reorder according to status or the client clock.

Page two returns the captured state even after allocation or expiry. The captured
evaluatedAt makes that limitation explicit; the frontend refreshes before an action.
Fresh writes always validate current owner state. A changed current settingsVersion
invalidates a cursor with RefreshRequired. A supplied stale runtime identity uses
the existing RuntimeReset/RuntimeNotReady rules before cursor lookup.

## Organization members

Public: `GET /api/v2/orgs/{orgID}/members`.
Private: `GET /internal/auth/v1/public/orgs/{orgID}/members`.
Capability: existing `membership.manage`, current own-organization Admin only.
Allowed query: `limit`, `cursor`, `state` (`active`, `revoked`, `all`; default `active`).

Join memberships to Users from one Auth aggregate snapshot. Return the existing
membership IDs, versions, org roles, assignment IDs/versions and union of assigned
roles. Add only `displayName` and required nullable `email`. Do not return login,
credentialVersion, verifier, session, proof, challenge, ciphertext or another
organization's assignments. A missing User edge is AuthStorageUnavailable, not an
invented contact. Sort by membershipID in UTF-8 byte order before paging.

The response identity is orgID, readerEpoch, aggregateRevision and evaluatedAt.
readerEpoch is a new unpredictable Auth process-instance UUID. The revision is the
captured aggregate revision, not a promise of current contact data on later pages.
Current role checks use current state, even if the captured member was an Admin.

## Organization invitations

Public: `GET /api/v2/orgs/{orgID}/invitations`.
Private: `GET /internal/auth/v1/public/orgs/{orgID}/invitations`.
Capability: existing `invitation.manage`, current own-organization Admin only.
Allowed query: `limit`, `cursor`, `state` (the five Invitation states or `all`;
default `all`). Sort by invitationID in UTF-8 byte order before paging.

Each row has the existing Invitation and `linkDelivery`: the latest accepted link
job's safe MailJob view, or null for a legacy invitation with no link job. Queued
maps to `pending` (or `retry` after a definite failed attempt), sending maps to
`sending`, delivered maps to `sent`; unknown, failed and suppressed retain their
names. This is a current observation in the captured
aggregate. It is not a delivery guarantee. OTP jobs cannot become linkDelivery.
Resolve ties in acceptedAt by the job's monotonically recorded enqueue order.

For a pending invitation with expiresAt <= evaluatedAt, return an `expired` read
projection and `resendAllowed=false`. Keep its persisted invitation version. This
projection does not commit an expiry transition or authorize acceptance. Other
states are returned as stored. `resendAllowed` is true only when pending, not
expired, and evaluatedAt >= resendAvailableAt. An action rechecks those facts.

No link, comparison-only account index, OTP, proof, ciphertext or transport error
is added to the collection. The nested predecessor Invitation contains its existing
comparisonEmail; removing that field would be a separate response change.
The response uses the same Auth page identity as members.

## Invitation link and mail

Auth configuration gains `invitationMail.frontendOrigin`. Require an absolute
HTTPS origin with no credentials, query, fragment or path other than `/`. Canonical
origin handling must preserve the existing exact Origin checks. A test-owned
configuration may allow HTTP only for explicit loopback hosts. Never derive this
origin from request Host, forwarded headers or a caller-supplied redirect URL.
Validate configuration before admission. An invalid or missing origin closes new
create/resend-link admission with 503 DeliveryUnavailable, before a mutation.
Existing invitation acceptance and OTP flows stay available.

The link is `<trusted origin>/invite/<encoded invitationID>`. Receiving or opening
it grants no identity, proof or membership. Keep the existing email-bound OTP,
preauth/session binding and atomic existing/new User acceptance. The frontend page
must avoid forwarding the ID to third-party resources and use a restrictive
Referrer-Policy. Building that page belongs to the frontend developer.

Create persists invitation, encrypted link payload, typed job, stable jobID and
command result/JobIDs in one aggregate replacement. It returns the existing 201
Invitation only after confirmed commit. The command's stored linkage proves the
job belongs to that original acceptance. GET invitations exposes the safe job view.
No SMTP call happens during the aggregate transaction.

`POST /api/v2/orgs/{orgID}/invitations/{invitationID}/resend-link` accepts the existing
Command body and session/Origin/CSRF rules. Its private counterpart is
`/internal/auth/v1/public/orgs/{orgID}/invitations/{invitationID}/resend-link`.
It requires current own-org Admin, a pending invitation with now < expiresAt,
and live invitation assignments. Accepted, expired, revoked and suppressed
invitations cannot enqueue a fresh job. It does not
require or create a challenge. A new accepted command increments invitation version,
sets resendAvailableAt to min(now + 60 seconds, expiresAt), and atomically enqueues
one new link job. Return DurableResult with that one safe MailJob view.
Keep the existing three accepted invitation creations per org/email per rolling
ten minutes, measured from their original seven-day expiry-derived creation time.
Add at most five accepted resend-link commands per invitation per rolling hour.
Admission limits do not count exact replays or definite failed commits. A new
command inside cooldown returns 429 RateLimited with the remaining Retry-After.

Before create/resend-link acceptance, apply the existing mail-issue ceiling of
three accepted jobs per canonical comparison-email target and 60 accepted jobs
globally per rolling ten minutes. Count both OTP and link jobs, including terminal
jobs accepted in that window; transport retries are not new issues. OTP admission
uses the same combined counter. Resolve a link target through its real invitation
edge rather than inventing a challenge. Do not decrypt mail payloads to rate-limit.
This intentionally consumes one shared mail-issue slot at invitation creation;
the explicit predecessor assertion transition map must cover this new fact.

Idempotency namespace is original actor, operation, org and commandID. Fingerprint
includes canonical recipient/assignments for create, invitationID for resend-link,
and the UA1 operation profile. Capture the configured origin in the encrypted job;
it is not a mutable fingerprint input. Same command and input returns the original
result with no second invitation/job after response loss or restart. Changed input
conflicts. Recheck current actor/scope before replay. Preserve commandID and payload
on ambiguous storage/transport outcome; command recovery uses the existing lookup.

Legacy create records made before UA1 remain replayable with their original
fingerprint/result and zero link jobs. A fresh resend-link command is the explicit
way to send their link. Never backfill jobs while replaying or opening old data.
Pre-UA1 records must be distinguished by persisted record format/profile, not by
guessing from empty jobs in a damaged UA1 record.

Add a typed mail purpose `invitation_link`; legacy missing purpose migrates only to
the existing OTP purpose when its challenge edge is valid. Link jobs bind directly
to invitation/org and have no fabricated challengeID. Update validation, monotonic
replacement rules, payload encryption, command linkage, fixture discovery and mail
dispatch together. A proposed Auth storage format v2 has an explicit v1→v2
migration. Reject unknown formats. Do not use a new field to weaken old guards.
Use new discriminated application types rather than turning CodeMail into optional
unrelated fields. Keep templates and fixtures outside production state.

The worker durably claims a job before IO and uses the existing SMTP transport/TLS
policy. Link payload carries the captured recipient, URL and original expiry.
Use a stable Message-ID derived from jobID for all its attempts. Delivery is bounded
at-least-once; SMTP can accept a message whose reply is lost. Unknown stays unknown,
does not become delivered or authorize a fresh command. Automatic retries retain
the same job and Message-ID, with at most three link-mail attempts and delays of
1 then 5 minutes, matching the existing OTP retry timing. This section applies to
invitation_link jobs; existing OTP retry timing and terminal policy stay unchanged.
A permanent definite failure is failed. Exhausted link-delivery unknown remains unknown
with no nextAttemptAt. Do not create more jobs for transport retries.

Before claim, recheck invitation expiry/state and all assigned restaurant scopes.
Expiry, cancel, acceptance or scope deletion suppress unclaimed link jobs, clear
encrypted payloads and prevent new claims. A job claimed before closure may finish
its bounded IO. Its result is recorded without restoring authority; the link still
cannot be accepted. Restarted sending jobs recover as unknown before retry.
Pending/unknown/delivered are distinct in admin observations. Store failures close
admission under the existing recovery rules. No secret or full URL enters logs.

The existing OTP resend must enforce invitation-level cooldown updated by link
resend, and link resend must enforce the same cooldown updated by OTP resend.
Each route retains its own job kind, challenge requirements and command namespace.
Neither route extends an invitation or challenge deadline.

## Errors and boundary checks

Use the existing Error schema and status/outcome rules. New reads use 401
Unauthenticated, 403 Forbidden, 410 ScopeDeleted, 409 RefreshRequired, 400
InvalidArgument and explicit 503 owner/storage/unavailable errors. Warehouse keeps
its existing runtime/settings errors. Link mutation uses existing invitation,
CommandConflict, RateLimited and ambiguous durable-write errors. It is never safe
to infer not_committed from a Gateway timeout.

Gateway validates public and private request/result schemas. Bind orgID,
restaurantID, productID and all nested resource identities to the authenticated
route. Validate matching capture identities across pages. Owners enforce current
authorization independently. Private Auth requires caller credential plus verified
session/activity headers. Private Warehouse requires both its existing per-caller
transport credential and the scoped JWT. Add the new operation IDs to the existing
Auth audience/permission policy and service caller ACLs with these exact capabilities.
Responses use Cache-Control: no-store. Set cookies only through the existing
Gateway session operations; these additions issue no new cookies.

## Acceptance and migration

[UA1 acceptance rows](api/ui-amendment-v1-acceptance.json) are proposed mandatory
rows. A schema example is not runtime evidence. Each row needs source regression
evidence and executable Nginx→Gateway→real owner evidence where specified.
Synthetic SMTP sinks are mandatory for local acceptance.

1. Freeze approved UA1 documents and hashes separately from the original C2.
2. Map every original all67 assertion affected by create-mail semantics to its
   original identifier and an explicit UA1 replacement assertion before editing
   the runner. Original cases/assertions/evidence remain unchanged as history.
3. Implement Warehouse reads, Auth reads/storage/mail, then Gateway projection.
4. Update owner README/CHANGELOG, setup, format migration and rollback instructions.
5. Run full build/lint/architecture/test/race and declared quality/module gates.
6. Run UA1 rows and the complete applicable predecessor corpus on identified source,
   image and configuration revisions. Report intentional predecessor expectation
   changes explicitly; do not label changed all67 as an unmodified original PASS.
7. Continue separate owner profile/F3/F4/J1/E/J2 work. UA1 approval and source checks
   do not close these stages or the frontend browser acceptance.

Upgrade stops the old Auth writer, makes a protected offline backup and uses the
real validated migration with retained encryption keys. No second identity writer.
New runtime/source identities must be qualified after restart. Old binaries must
reject v2 data. Rollback keeps admission closed until a compatible writer can read
every acknowledged command/job; restoring an old backup must not discard them.
No deployment or migration of the user's live authority is part of this draft.
