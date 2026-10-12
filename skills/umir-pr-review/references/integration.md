# UMIR integration review criteria

Use only the rows relevant to the changed contract, implementation or artifact. The [handoff specification](../../../docs/gateway-frontend-handoff.md) and [business-core](../../../docs/business-core.md) contain maintained requirements; this reference points to the failure paths most likely to cross repositories.

| Boundary | Trace and evidence |
| --- | --- |
| Proxy construction | Router accepts independently injected serving/warehouse handlers; transport is replaceable. Env/config stays in composition. No foreign internal or business implementation imports. |
| HTTP forwarding | Both health routes and prefix removal remain. Method, remaining path, query, body, end-to-end headers and upstream status/body survive; IDs with Unicode/reserved characters remain opaque. |
| Write failure | For a lost/malformed POST response, locate the persisted frontend attempt and exact retry. No automatic new commandID, fabricated success or gateway retry loop. Cancellation is not proof of absent commit. |
| Business outcomes | Check OrderConfirmed has the recorded order; RequiresConfirmation has the replacement; pending/superseded/empty/conflict/unavailable are explicit. HTTP 2xx alone is insufficient. |
| Resolve authority | Missing lookup/read error does not clear pending. Terminal not_committed must come from the owner with prevention of late writes. A mock result is not proof of that production guarantee. |
| Numeric representation | Test exact `9007199254740993` through fixtures, HTTP, parsing and display. Quantity, portions, lineNo, versions and sequence use the defined decimal strings; Number conversion cannot preserve every int64. |
| Dates | Lexical date-only rejection happens before time.Time conversion; full midnight is valid. Display uses Moscow time; frontend does not recalculate eligibility. |
| Proposal composition | Original lineNo and duplicate dishID survive. Preparation reserves nothing. Stock growth does not raise the accepted order; reduced replacements require explicit confirmation. |
| Menu/recommendations | Menu has no available portions. Independent dish estimates are not a jointly available order. Reasons show actual per-batch quantities, including tied expiries in defined order. |
| HTTP/browser topology | Browser calls frontend → gateway → real serving/warehouse processes. Unit handler/transport injection can isolate failures, but does not prove the real HTTP or browser path. Test data and fault fixtures stay outside production src/internal and runtime images. |
| Design/UI | Inspect editable Figma components and changed states at desktop/tablet/mobile. Confirm keyboard/focus/error guidance, loading/empty/unavailable separation and pending recovery. Missing images do not justify invented clipping/color findings. |
| Integration level | Distinguish real Runtime bridge tests, real-process HTTP acceptance and browser acceptance. Preserve exact service revisions, controlled failure evidence and process-local memory limits. Passing these levels does not establish durable recovery after process restart. |

For gateway changes, relevant checks include Go formatting, test-race/vet, HTTP handler/transport tests and the unchanged health/proxy regressions. For frontend, inspect the declared package scripts and run the implemented unit/browser/build checks. For an OpenAPI or fixture change, validate all changed variants against the same revision. For documentation-only work, validate the affected links and instructions without launching an unrelated stack.

For a combined PR set, link the gateway, frontend and factory PRs and use one agreed API revision. A reviewer can approve the covered Runtime or HTTP scope while frontend/browser work remains unfinished. Merge, image selection and deployment are separate authorized actions.
