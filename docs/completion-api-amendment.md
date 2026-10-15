# Completion API amendment FIN1

Date: 2026-10-15, Europe/Moscow. Version: `1.0.0-draft.1`.

This is the contract supplement required by FIN-11/12 in the [sole current plan](current-plan.md). It is not another execution plan. It defines only supply suggestions and the revised existing recommendations. FEFO display continues to use the existing UA1 product-batch and recorded-order allocation contracts.

The numeric defaults below are implementation decisions. The user selected the feature scope; the earlier examples did not select these defaults. The supplement is reviewed and versioned before owner/consumer integration. C2, UA1 source artifacts, their hashes and historical evidence remain unchanged. The generated composed FIN1 profile has `info.version=2.0.0-FIN.1` and records its exact inputs.

## Fixed policy

| Parameter | FIN1 value |
| --- | --- |
| Consumption window | Previous 7 physical days, ending at the captured owner instant |
| Low coverage | Strictly less than 3 days |
| Target stock | 7 days of observed average consumption |
| Recommendation horizon | 72 hours |
| Urgency decay τ | 12 hours |
| Recommendation count | At most 10 |
| Demand history window | The same previous 7 physical days |
| Fallback demand | 2 portions per hour per dish |

These are fixed versioned defaults, without new settings screens or environment configuration. A day is 24 physical hours. The interval is `[evaluatedAt−168h, evaluatedAt)`. An event exactly at the start belongs to the window; an event at the end does not. Calculations use recorded instants. Organization timezone controls presentation, not the length of this rolling interval.

## Supply suggestions

Warehouse owns the calculation and the read. Public operation `supplySuggestions` is `GET /api/v2/r/{restaurantID}/warehouse/supply-suggestions`; its private counterpart is `GET /internal/warehouse/v2/r/{restaurantID}/supply-suggestions`. It requires the existing `stock.read` capability, a current scope and the usual service authentication. Waiter alone does not receive it.

Capture the current validated state and matching event prefix once. The read does not append events, confirm writeoffs, reserve stock or create deliveries. It must not rebuild state by replaying the complete journal or clone the complete growing inventory state. It may scan the captured journal to derive period facts. No mutable state or journal aliases leave the owner.

For each product:

1. Sum actual `OrderPlaced` allocation quantities and `BatchWriteoffConfirmed` quantities whose event `RecordedAt` belongs to the window. Do not recompute old orders from current recipes. `BatchExpiryPending` is not consumption and is not counted again. Legacy automatic expiry is not a human-confirmed writeoff.
2. Sum positive current batch quantities only when the batch is not pending writeoff and `evaluatedAt < expiresAt`. Pending or expired stock is ineligible even before the scheduler records a transition.
3. Compute observed average consumption as period quantity divided by 7. The denominator remains 7 for incomplete history. This is an observed-window estimate; it does not invent earlier usage.
4. With positive consumption, compare coverage exactly as `available×7 / consumed < 3`. Equality does not trigger a suggestion. Zero consumption has no finite coverage estimate.
5. For each current recipe, calculate a one-portion product deficit `max(recipeQuantity−available, 0)`. Aggregate a product's deficits with `max`, not addition. This restores an individual portion opportunity; it does not promise that all affected dishes can be prepared together.
6. Include a product when coverage is low or its maximum one-portion deficit is positive. Suggest `max(ceil(consumed×7/7)−available, maximumDeficit, 0)`, with the result rounded upward to the existing minimum quantity quantum. The general formula uses the declared target/window values even when they are equal in FIN1.

With no usage, enough stock and no deficit, return no suggestion. With no history and a real deficit, suggest only the deficit. Keep the existing epoch history-coverage facts: requested start, coverage start, available start and `complete`. Never relabel partial history as complete. The UI shows that the calculation uses incomplete history.

Quantity arithmetic is exact and unit-specific. Use checked sums; do not add grams, millilitres and pieces. Intermediate products and ratios may use arbitrary-precision integers/rationals. A final quantity outside the existing positive int64 quantity contract returns `CalculationUnavailable`, without state change.

Rows contain product ID/name/unit, eligible stock, observed period consumption, average consumption in base units/day, nullable coverage days, one-portion deficit, suggested quantity, affected dish IDs and deterministic reason codes `low_coverage` / `stop_list_deficit`. Mean and coverage are finite decimal display strings rounded to six places; policy comparisons and target quantities use exact arithmetic. Sort by product ID. More than 500 result rows or an oversized response fails with the existing typed capacity/calculation error; do not silently truncate or claim a complete result.

## Recommendation facts and ownership

Serving remains the sole ranking owner. Warehouse supplies one coherent read of existing menu evaluation, eligible expiring-batch facts and aggregate dish demand from the captured `OrderPlaced` period. Its new private operation `recommendationInputs` is `GET /internal/warehouse/v2/r/{restaurantID}/recommendation-inputs`.

The read uses the existing expiring-batch rules and current menu evaluation. Every fact has the same restaurant, epoch, settings, evaluatedAt and state version. It performs no stock mutation or full state replay. Do not expose raw orders, user/session data or unrelated history.

Add the narrow `consumer.recommendations.read` permission for this Serving-to-Warehouse read. Auth includes it only in the existing `recommendations` authorization policy, with the existing public `recommendations.read` capability and audience binding. It grants no stock management, arbitrary history, delivery or writeoff capability. Gateway keeps its current public recommendation route. All participants use the composed FIN1 schemas; predecessor clients are not silently declared compatible with the new strict response shape.

For a dish, demand is its actual placed portions in the full 168-hour window divided by 168 hours when coverage is complete and that dish has recorded orders. With incomplete coverage or no orders for that dish, use exactly 2 portions/hour. Report `history` or `fallback` as the demand basis. This is ranking input, not a sales forecast guarantee.

## Ranking and explanation

Use only eligible batches with `evaluatedAt < expiresAt <= evaluatedAt+72h`. Exclude unavailable dishes and dishes with no risky ingredient. For each available dish/product pair:

- `W` is all eligible risk quantity for that product within the horizon.
- `r` is the complete recipe quantity for one portion, including allocations from later fresh batches. Do not substitute only the tiny allocation from the nearest-expiring batch.
- `t` is time until that product's earliest risky batch expires.
- `C(t)` is the declared demand rate multiplied by that physical interval.
- `U=τ/(τ+t)`, `A=min(1,W/r)`, `E=min(1,C(t)×r/W)`.
- The dish score is the sum of `U×A×E` over its risky products.

Compute with exact rationals. Rank by exact score descending, nearest risk expiry ascending, then dish ID. Emit a finite six-decimal score string; wire rounding does not decide ties. Keep at most 10 results.

Each reason keeps product, unit, nearest risky batch and expiry, and adds the aggregate risky quantity, whole recipe quantity per portion and `ceil(W/r)` target portions. The target explains how many portions would use the risky quantity. It does not promise that all other ingredients can supply those portions. Remaining seconds are derived from the captured instant, without NaN/infinity or cross-unit sums.

The mandatory comparison fixture gives fish 3 kg at risk, fish recipe 300 g/portion, and parsley 1 g at risk with 5 g/portion plus sufficient later fresh parsley. Both dishes are available; risky expiries are equal at six hours; fallback demand is fixed. Fish scores above parsley because parsley's risky remainder contributes only a fraction of a portion. Rice expiring in six months is outside the horizon. When normalized factors truly tie, use the declared expiry/dish-ID tie break, not raw ingredient mass.

Do not add competition penalties, alternative ingredient costs, automatic discounts, supplier/pricing data, new analytics pages or a separate recommender service.

## Response and failure rules

The OpenAPI overlays and composed profiles define exact required fields and closed object shapes. Existing decimal integer quantity strings stay unchanged. Display decimals are strings, without exponential notation. Scoped contexts use the existing canonical IDs, epochs, settings and version strings. Consumers reject unknown variants, mismatched capture identities and non-finite/malformed numeric strings.

Keep the existing typed errors for invalid input, absent/removed scope, missing capability, runtime reset/not-ready, settings change, capacity/calculation failure and unavailable upstreams. A read failure is not a successful empty result. Cancelled or stale scope requests cannot install data into a later selected restaurant. Reads remain read-only.

## Acceptance and policy transition

The [acceptance definitions](api/completion-v1-acceptance.json) assign `FIN-M6-01` through `FIN-M6-12` and `FIN-REC-01` through `FIN-REC-14`. Embedded fixture setups are normative inputs, not executed results. The [policy transition](api/completion-v1-transition.json) identifies affected predecessor predicates. Their fixtures and assertions cover:

- exact period boundaries and actual RecordedAt;
- eligible vs expired/pending stock and no expiry/writeoff double count;
- zero/empty/incomplete history and coverage labels;
- threshold below/equal/above 3 days, one-portion deficits and maximum aggregation;
- rounding, mixed units, checked overflow, response bounds and no mutations;
- coherent scope/epoch/settings/prefix and capability refusals;
- risk horizon boundaries, unavailable/no-risk dishes and whole-recipe quantities;
- fish/parsley/rice, W<r, W=0, demand source, exact ties, top 10 and finite serialization;
- strict Gateway/client decoding and real UI rendering in the existing tabs.

Old C2 recommendation order and reason predicates describe the predecessor policy. Record their transition to these FIN1 predicates explicitly. Keep the old artifacts and results; do not rewrite them as FIN1 PASS. Existing unrelated C2/UA1 guarantees and complete repository quality gates still apply. This does not resume the archived all34/all67/F3/F4/J1/E/J2 execution backlog.
