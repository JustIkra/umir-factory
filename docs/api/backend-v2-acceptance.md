# Backend v2 acceptance and conformance guide

F1 supplement, 2026-10-13, Europe/Moscow. These documents add examples and acceptance targets to the C2 frozen contract. They do not amend its schemas or demonstrate running service behavior. F2/F3 and the service lanes consume the identity below; each lane verifies its own execution tier before source writes.

## Frozen input and compatibility

Contract version: **2.0.0-draft.2**. C2 approved content identity: **sha256:733e102ff0b4f52bb49d435716f9f1720642c8fad3a8a5d0685847071950e9e6**.
C1 manifest SHA-256: `4b3536206cc94c411f04c737b961348e6284b2444ce142daae0745c5eae36e1d`. Resolved decision record SHA-256: `623805c4bda3ea18be2720270430bdea8677ede26bcc4fae4ec1aa8c3f1cb29c`.

[Public OpenAPI](backend-v2-public.json), [private OpenAPI](backend-v2-private.json), [frozen examples](backend-v2-examples.json) and [relational rules](backend-v2.md) are canonical. The frozen set also includes `docs/business-core.md`. Their byte identities remain unchanged. Additions here have separate file hashes in F1 evidence. Any required protocol correction returns to the explicit contract amendment gate; do not patch a schema during a service or runner task.

| Frozen file | SHA-256 |
| --- | --- |
| docs/api/backend-v2-public.json | `e710150288d650965cfee80b7cb623f5466583c03da9edf5561f94d127970897` |
| docs/api/backend-v2-private.json | `f1a46a199c5c616523ff24ed0c43a0ae92b43edd93e1264a854fd766030aa1c1` |
| docs/api/backend-v2-examples.json | `ac2f52f5d8667e54f1000eda0fbbd03356d990e9756a5a1982b3308ff206c6ed` |
| docs/api/backend-v2.md | `08a606696c1091096241df7a18120473f2e22bb6c60d04483732327d231b616f` |
| docs/business-core.md | `8e64f2ed3200cbdcf666f762107bf65c62e94273694fc857dc2428037051e011` |

The selected legacy `openapi.yml`, `warehouse-public.yml` and `warehouse-internal.yml` remain intact. Selected acceptance continues independently. New business routes require `/api/v2/r/{restaurantID}`; old unscoped business paths in the backend topology reject without dispatch. Operator aliases preserve their operator/session shapes. No frontend, selected image change, publication or release is part of this supplement.

## How to consume the examples

[Supplementary conformance cases](backend-v2-conformance.json) contains exact JSON values and positive/negative expectations against frozen schema names. Its wire examples bind operationID, method, route, path/query/header values, body and typed status/result. Example cookies, service keys and scope IDs are synthetic documentation fixtures. They are not a signed token or bootstrap mechanism. Install real credentials through the supported test bootstrap when F3 executes a case. An array query such as summary restaurantID is serialized as repeated keys; other duplicate query keys are rejected. Percent-encode opaque catalog IDs once. Use header names exactly as declared.

Public Auth/org/restaurant operations run through Gateway cookie/Origin/CSRF controls; private Auth facades use their caller key and browser context headers. Auth recomputes each business capability set and signs the exact target token. Private business listeners also require the configured per-caller key. Forwarded Waiter requests retain their user, restaurant and operation; only their specified consumer routes are authorized. Schemas reject many malformed shapes; issuer/signature/time, current access, caller allowlist and audience/operation pairing require live checks.

Lifecycle creation returns registering until current Warehouse/Serving/Gateway epochs are acknowledged. Accepted deletion returns 202/deleting/pending and revokes new scope decisions immediately. Its durable UUID operationID is separate from opaque commandID. Each restaurant has exactly three associated readiness entries in declared order; unavailable peer status stays pending without an invented epoch. Exact replay returns the original accepted result; polling returns current cleanup. Only the initiating User with valid global session or an authorized own-org Admin can poll after cutoff.

Writeoff is human-only. At deadline residual stock becomes pending, with unchanged physical quantity and one pending fact. A passive lookup neither prepares expiry nor appends. Its commandID/batchID/original Warehouse epoch and canonical fingerprint must agree; unseen and pending stay unknown. A reset returns RuntimeReset/indeterminate before lookup, even if IDs are reused. Retrying a different command never resolves an unknown original action. Lookup transport failure is unavailable about the observation, without proving the original outcome.

Stock and movements carry independent captured identities, exact units and strings. Stock ignores the movement period; movements count only committed RecordedAt facts in [from,to). Pending, proposed, rejected, unknown and duplicate facts contribute nothing. Org summary uses explicit authorized restaurant reads and completes all pages; failed paging invalidates that entire restaurant result, without zero reports or unit merging. A 503 after committed stock preparation still preserves committed version/invalidation for the correct tenant. Private metadata is stripped from browser responses.

Organization settings control civil inputs and calendar ranges, with packaged IANA data and a captured settings version. Physical day shelf life is 24 hours; calendar month clamps and selects earlier fold/first valid gap. Manual gaps reject and folds need explicit choice. Date reports use first-valid day starts and validate missing successor dates. Existing instants and Auth TTLs do not change. Retained exact retry resolves original captured settings before a stale fresh-form check; current access remains mandatory.

App Feedback receives verified private context. Anonymous intake is explicitly unscoped even with a cookie. The durable namespace includes app User/org/restaurant or anonymous, then submissionID. Lost reply/restart reuses the original intake and job IDs without retargeting. Legacy records migrate unscoped. Own-org Admin read/export is per restaurant through Gateway; employees can submit but cannot read/export. Synthetic sinks verify SMTP/Telegram privacy. Intake/job creation does not promise exactly-once mail delivery.

## Acceptance map and executable assertions

[Acceptance map](backend-v2-acceptance.json) assigns stable BE-E1..E5 IDs. Each requirement names setup, action, real operation IDs, source anchor, executable equality/absence assertions and evidence to retain. The operations array inventories every frozen public/private method and request/result schema. The generic shape ID covers request/response validation for every operation; scenario IDs add relational behavior. An inventory row is not a claim that the operation ran. For live 200, text/event-stream carries LiveEvent: decode each event data as one JSON object, validate it against the frozen LiveEvent schema, and require the SSE event name to equal its type. Validate each change id as an opaque cursor; access-ended and refresh-required carry no change cursor. The stream is not one JSON response body. Compare response schema bindings for all media types; null denotes a bodyless response.

Pointers refer to **normalized test evidence**, not new API fields. Equality compares exact typed values, arrays and decimal strings. Absence distinguishes missing from null. Each normalizer must retain its raw source facts and extraction procedure; booleans must be measured from those facts. For example, quantity.onHand comes from the stock row, journal.pendingFacts from matching tenant/epoch/event type facts, and dispatch.businessCalls from bounded transport observations. Session deadline unchanged requires before/after persisted instants; noDataAfterDeniedCheck requires stream bytes and observed authorization checks. Concurrent winners and job counts come from owner outcomes and journal identities, not HTTP status alone. Missing required observations fail the case. Absence requires a captured parent evidence object and an extractor capable of observing that field; an empty object cannot prove no leak.

BE-E3-12 observes already-expired intake at its admission boundary before any read can prepare expiry. Its normalizer compares the batch settings/tzdata with captured owner data, checks joint journal/projection publication, and distinguishes definite precommit refusal from an unknown transport outcome. BE-E4-18 counts distinct live captures across stock and movements in one tenant. It verifies all 128 existing reports survive exhausted-capacity 503, separate tenant capacity, and release after five physical minutes. These cases require retained raw admission/report-store and clock evidence, not status-only observations.

F1 executes schema validation and mutation checks of these assertion specifications with synthetic test observations. F3 implements normalizers and real scenario orchestration. J1 freezes the assembled source/build identities; E1-E5 execute these scenarios against the real five services. Every E group owns its project, ports, Auth JSON/Feedback journal and clock fixtures. Capture actual clock instants and quantities, failure/retry observations, journals and cleanup. Never replace unavailable evidence with host time, an expected value or a successful empty response.

### E1: Access, roles and browser boundary

| ID | Executable requirement | Operations |
| --- | --- | --- |
| BE-E1-01 | Validate the exact request and response for every documented operation | All 145 operations; see the JSON inventory |
| BE-E1-02 | One global session across permitted restaurants | `login`, `me`, `access`, `inventory`, `menu`, `privateinventory`, `privatemenu`, `authFacadelogin`, `authFacademe`, `authFacadeaccess` |
| BE-E1-03 | Cross-scope substitution and replay refusal | `inventory`, `receiveDelivery`, `confirmOrder`, `live`, `appFeedback`, `privatereceiveDelivery`, `privateinventory`, `privateconfirmOrder` |
| BE-E1-04 | Role union stays inside its restaurant | `confirmWriteoff`, `confirmOrder`, `analyticsstock`, `recommendations`, `privateconfirmWriteoff`, `privaterecommendations`, `privateconfirmOrder`, `privateAnalyticsstock` |
| BE-E1-05 | Own-org Admin and global org creation | `createOrganization`, `organization`, `createRestaurant`, `updateMembership`, `authFacadecreateOrganization`, `authFacadeorganization`, `authFacadecreateRestaurant`, `authFacadeupdateMembership` |
| BE-E1-06 | Last active Admin is protected atomically | `updateMembership`, `revokeMembership`, `revokeAssignment`, `authFacadeupdateMembership`, `authFacaderevokeMembership`, `authFacaderevokeAssignment` |
| BE-E1-07 | Invitation acceptance binds identity and roles | `createInvitation`, `beginInvitationChallenge`, `verifyChallenge`, `completeInvitedUser`, `acceptInvitation`, `authFacadecreateInvitation`, `authFacadebeginInvitationChallenge`, `authFacadeverifyChallenge`, `authFacadecompleteInvitedUser`, `authFacadeacceptInvitation` |
| BE-E1-08 | Cookie, Origin and synchronizer CSRF boundary | `csrf`, `login`, `logout`, `displayName`, `password`, `anonymousFeedback`, `authFacadecsrf`, `authFacadelogin`, `authFacadelogout`, `authFacadedisplayName`, `authFacadepassword` |
| BE-E1-09 | Auth unavailable closes new protected requests | `inventory`, `menu`, `appFeedback`, `authorize`, `accessCheck`, `privateinventory`, `privatemenu` |
| BE-E1-10 | Foreground idle persistence precedes dispatch | `inventory`, `me`, `accessCheck`, `privateinventory`, `authFacademe` |
| BE-E1-11 | Scoped revoke keeps session elsewhere | `revokeAssignment`, `access`, `inventory`, `live`, `privateinventory`, `authFacadeaccess`, `authFacaderevokeAssignment` |
| BE-E1-12 | Verified app and explicitly anonymous Feedback | `appFeedback`, `anonymousFeedback`, `privateAppFeedback`, `privateAnonymousFeedback` |
| BE-E1-13 | Feedback reads and exports need own-org Admin | `publicFeedbackRead`, `publicFeedbackExport`, `feedbackRead`, `feedbackExport` |
| BE-E1-14 | Strict parsing and unscoped compatibility | `registerProduct`, `prepareOrder`, `compatgetCurrentOperator`, `compatsignIn`, `privateregisterProduct`, `privateprepareOrder` |

### E2: Private authorization, registry and live access

| ID | Executable requirement | Operations |
| --- | --- | --- |
| BE-E2-01 | JWT validation and trust boundary matrix | `authorize`, `consumerEvaluate`, `privateinventory` |
| BE-E2-02 | Serving forwarding cannot widen Waiter access | `menu`, `prepareOrder`, `confirmOrder`, `consumerMenu`, `consumerEvaluate`, `consumerCommit`, `consumerCommand`, `consumerOrder`, `consumerAttempt`, `privatemenu`, `privateprepareOrder`, `privateconfirmOrder` |
| BE-E2-03 | Private transport key and rotation fail closed | `jwks`, `authorize`, `serviceToken`, `anonymousToken` |
| BE-E2-04 | Registration partial readiness and current epochs | `createRestaurant`, `registry`, `ackReadiness`, `warehouseRegister`, `servingRegister`, `gatewayRegister`, `authFacadecreateRestaurant` |
| BE-E2-05 | Idempotent registration and after-start creation | `warehouseRegister`, `servingRegister`, `gatewayRegister`, `createRestaurant`, `authFacadecreateRestaurant` |
| BE-E2-06 | Incomplete registry and reset cannot imply empty success | `registry`, `warehouseRuntime`, `servingRuntime`, `gatewayRuntime` |
| BE-E2-07 | Lifecycle peers remain completely associated | `lifecycle`, `authFacadelifecycle`, `ackReadiness` |
| BE-E2-08 | SSE scope, cursor, expiry and denial termination | `live`, `accessCheck` |

### E3: Warehouse and Serving

| ID | Executable requirement | Operations |
| --- | --- | --- |
| BE-E3-01 | First-free IDs do not reserve | `suggestID`, `privatesuggestID` |
| BE-E3-02 | Catalogs, dishes and deliveries stay isolated | `products`, `registerProduct`, `dishCatalog`, `dishDetails`, `defineDish`, `receiveDelivery`, `servingProducts`, `servingDishes`, `servingDish`, `privateproducts`, `privateregisterProduct`, `privatedishCatalog`, `privatedishDetails`, `privatedefineDish`, `privatereceiveDelivery`, `privateservingProducts`, `privateservingDishes`, `privateservingDish` |
| BE-E3-03 | Recommendations are actual FEFO allocations without reservation | `recommendations`, `menu`, `stopList`, `consumerMenu`, `privatemenu`, `privatestopList`, `privaterecommendations` |
| BE-E3-04 | Order confirmation uses actual recheck and passive unknown semantics | `prepareOrder`, `confirmOrder`, `resolveOrder`, `consumerEvaluate`, `consumerCommit`, `consumerCommand`, `consumerOrder`, `consumerAttempt`, `privateprepareOrder`, `privateconfirmOrder`, `privateresolveOrder` |
| BE-E3-05 | At deadline expiry is pending and physical | `dueExpiry`, `privateexpiringBatches`, `privatePendingWriteoffs`, `inventory`, `expiringBatches`, `pendingWriteoffs`, `privateinventory` |
| BE-E3-06 | Fresh stock prepares once before scheduler tick | `analyticsstock`, `privateAnalyticsstock`, `inventory`, `consumerMenu`, `privateinventory` |
| BE-E3-07 | One scheduler independent of browser and never human writeoff | `serviceToken`, `dueExpiry`, `privateconfirmWriteoff` |
| BE-E3-08 | Human final writeoff once, replay and competing commands | `confirmWriteoff`, `privateconfirmWriteoff` |
| BE-E3-09 | Passive writeoff lookup binds fingerprint without side effects | `resolveWriteoffCommand`, `privateresolveWriteoffCommand` |
| BE-E3-10 | Lookup transport and reset preserve unknown old outcome | `resolveWriteoffCommand`, `privateresolveWriteoffCommand`, `consumerCommand`, `consumerOrder`, `consumerAttempt` |
| BE-E3-11 | Historical expiry payload is still final writeoff | `history`, `privatehistory` |
| BE-E3-12 | Already-expired delivery and pending transition admit atomically | `receiveDelivery`, `privatereceiveDelivery`, `inventory`, `privateinventory`, `pendingWriteoffs`, `privatePendingWriteoffs`, `history`, `privatehistory`, `updateSettings`, `authFacadeupdateSettings` |

### E4: Analytics and timezone

| ID | Executable requirement | Operations |
| --- | --- | --- |
| BE-E4-01 | Stock accounting and risk use server cutoff | `analyticsstock`, `privateAnalyticsstock` |
| BE-E4-02 | Movements use RecordedAt half-open range and actual committed facts | `analyticsmovements`, `privateAnalyticsmovements` |
| BE-E4-03 | Server filter, pagination and cursor capture | `analyticsstock`, `analyticsmovements`, `privateAnalyticsstock`, `privateAnalyticsmovements` |
| BE-E4-04 | Incomplete history is explicit after runtime reset | `analyticsmovements`, `privateAnalyticsmovements` |
| BE-E4-05 | Unknown schemas and arithmetic failure do not vanish | `analyticsmovements`, `analyticsstock`, `privateAnalyticsstock`, `privateAnalyticsmovements` |
| BE-E4-06 | Committed preparation survives following analytics failure | `analyticsstock`, `analyticsmovements`, `live`, `privateAnalyticsstock`, `privateAnalyticsmovements` |
| BE-E4-07 | Own-org summaries preserve separate reports and errors | `orgSummary` |
| BE-E4-08 | Summary gathers all 501 rows without truncation | `orgSummary` |
| BE-E4-09 | Org timezone authority and isolation | `updateSettings`, `organization`, `analyticsmovements`, `privateAnalyticsmovements`, `authFacadeorganization`, `authFacadeupdateSettings` |
| BE-E4-10 | Calendar days are not physical 24-hour periods | `analyticsmovements`, `privateAnalyticsmovements` |
| BE-E4-11 | Civil inputs require explicit fold and reject gap | `receiveDelivery`, `privatereceiveDelivery` |
| BE-E4-12 | Shelf-life day24h and month clamp/fold/gap | `receiveDelivery`, `privatereceiveDelivery` |
| BE-E4-13 | Timezone changes preserve facts, FEFO and TTL instants | `updateSettings`, `history`, `inventory`, `recommendations`, `me`, `privateinventory`, `privatehistory`, `privaterecommendations`, `authFacademe`, `authFacadeupdateSettings` |
| BE-E4-14 | Retained exact retries precede stale settings rejection | `receiveDelivery`, `confirmOrder`, `confirmWriteoff`, `updateSettings`, `privatereceiveDelivery`, `privateconfirmWriteoff`, `privateconfirmOrder`, `authFacadeupdateSettings` |
| BE-E4-15 | In-flight settings and lost epoch ordering | `receiveDelivery`, `confirmOrder`, `analyticsmovements`, `privatereceiveDelivery`, `privateconfirmOrder`, `privateAnalyticsmovements` |
| BE-E4-16 | Missing tzdata fails readiness and reports explicitly | `warehouseRuntime`, `servingRuntime`, `gatewayRuntime`, `receiveDelivery`, `analyticsmovements`, `privatereceiveDelivery`, `privateAnalyticsmovements` |
| BE-E4-17 | Analytics defaults and bounds | `analyticsstock`, `analyticsmovements`, `orgSummary`, `privateAnalyticsstock`, `privateAnalyticsmovements` |
| BE-E4-18 | Captured report admission is bounded per tenant and expires | `analyticsstock`, `analyticsmovements`, `privateAnalyticsstock`, `privateAnalyticsmovements` |

### E5: Restart, cascade and durable delivery

| ID | Executable requirement | Operations |
| --- | --- | --- |
| BE-E5-01 | Auth aggregate survives commit/lost reply restart | `createOrganization`, `createInvitation`, `updateMembership`, `authCommandLookup`, `authFacadecreateOrganization`, `authFacadeupdateMembership`, `authFacadecreateInvitation`, `authFacadeauthCommandLookup` |
| BE-E5-02 | OTP, invitation and outbox atomic persistence | `beginInvitationChallenge`, `resendChallenge`, `verifyChallenge`, `completeInvitedUser`, `beginEmailChallenge`, `completeEmailChallenge`, `authFacadebeginInvitationChallenge`, `authFacaderesendChallenge`, `authFacadeverifyChallenge`, `authFacadecompleteInvitedUser`, `authFacadebeginEmailChallenge`, `authFacadecompleteEmailChallenge` |
| BE-E5-03 | Mail unknown delivery does not promise exactly once | `createInvitation`, `resendInvitation`, `resendChallenge`, `authFacadecreateInvitation`, `authFacaderesendInvitation`, `authFacaderesendChallenge` |
| BE-E5-04 | Auth storage faults and migration fail closed | `login`, `authorize`, `authCommandLookup`, `authFacadelogin`, `authFacadeauthCommandLookup` |
| BE-E5-05 | Session deadlines, capacity and global revocation | `login`, `logout`, `logoutAll`, `password`, `me`, `authFacadelogin`, `authFacadelogout`, `authFacadelogoutAll`, `authFacademe`, `authFacadepassword` |
| BE-E5-06 | Password and login throttles preserve prior policy | `login`, `password`, `authFacadelogin`, `authFacadepassword` |
| BE-E5-07 | Invitation and OTP bounds, comparison and one-use binding | `createInvitation`, `resendInvitation`, `cancelInvitation`, `verifyChallenge`, `acceptInvitation`, `authFacadecreateInvitation`, `authFacaderesendInvitation`, `authFacadecancelInvitation`, `authFacadeverifyChallenge`, `authFacadeacceptInvitation` |
| BE-E5-08 | Business restart is empty and old attempts indeterminate | `inventory`, `history`, `resolveOrder`, `resolveWriteoffCommand`, `registry`, `privateinventory`, `privatehistory`, `privateresolveOrder`, `privateresolveWriteoffCommand` |
| BE-E5-09 | Restaurant/org cascade accepts cutoff before cleanup | `deleteRestaurant`, `deleteOrganization`, `lifecycle`, `live`, `authFacadedeleteOrganization`, `authFacadedeleteRestaurant`, `authFacadelifecycle` |
| BE-E5-10 | Deletion replay recovers immutable polling identity | `deleteRestaurant`, `deleteOrganization`, `lifecycle`, `authCommandLookup`, `authFacadedeleteOrganization`, `authFacadedeleteRestaurant`, `authFacadelifecycle`, `authFacadeauthCommandLookup` |
| BE-E5-11 | Tombstone wins registration and cleanup retry races | `warehouseRegister`, `warehouseClose`, `servingRegister`, `servingClose`, `gatewayRegister`, `gatewayClose`, `registry` |
| BE-E5-12 | Deleted invitation and late command cannot restore access | `acceptInvitation`, `completeInvitedUser`, `consumerCommit`, `confirmOrder`, `privateconfirmOrder`, `authFacadecompleteInvitedUser`, `authFacadeacceptInvitation` |
| BE-E5-13 | Feedback namespaces and jobs survive restart | `appFeedback`, `anonymousFeedback`, `privateAppFeedback`, `privateAnonymousFeedback` |
| BE-E5-14 | Feedback migration, privacy and retained facts | `publicFeedbackRead`, `publicFeedbackExport`, `feedbackRead`, `feedbackExport` |
| BE-E5-15 | Auth command namespace and cookie recovery | `authCommandLookup`, `login`, `completeInvitedUser`, `completeEmailChallenge`, `authFacadelogin`, `authFacadecompleteInvitedUser`, `authFacadeauthCommandLookup`, `authFacadecompleteEmailChallenge` |

## Validation and lane handoff

The baseline Factory Makefile, scripts and CI declare no OpenAPI/JSON Schema validation command. Existing Go quality commands are not schema validators. F1 uses the C1 isolated validation tooling (Ajv 2020, ajv-formats and Swagger Parser, with recorded lockfile identity) without changing module dependencies or CI. F1 evidence retains the actual command, versions, red failure, green schema/wire checks, assertion mutation checks and preservation manifest.

From the Umir-hack workspace, rerun the task-owned check:

```sh
node .memory-base/plans/backend-e2e/runs/backend-e2e-20261012/F1/check.cjs
```

This check needs the frozen C1 manifest/C2 record and C1 validation-tools installation. It is workspace evidence tooling, not a newly declared Factory command. A standalone consumer can load both OpenAPI 3.1 JSON files into an Ajv 2020 registry, compile every component, validate frozen plus supplementary schemaCases, then validate each wire request/response against its named operation. Apply the frozen relational rules separately. Pin and record the consumer's schema tooling; absence of it is incomplete, not a passed schema gate.

F1 is JSON/Markdown only: no concurrent code or applicable race test. Complete Factory build/lint/architecture/race/module gates occur at F4; service-dependent assembly gates occur at J1. F2 owns runner/topology, F3 owns tests/http scenarios, and F4 owns README/CHANGELOG and the preserved C0 overlay reconciliation. No peer unfinished source is required for this handoff. Runtime acceptance, deliveries, restarts and SSE assertions remain unexecuted by F1.
