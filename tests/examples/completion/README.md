# Isolated completion demo

The supported application assembly is `compose.completion.yaml` at the Factory root. This directory owns synthetic demo data and test-only mail. The base application uses the production Auth, Gateway, Warehouse, Serving and Frontend images. It does not load fixture service binaries or require Feedback.

`prepare-mail.mjs` creates fresh private TLS and authenticated SMTP inputs for a dedicated `umir-completion-*` test directory. `compose.mail.yaml` adds Mailpit only to the explicit isolated test recipe. It accepts `.invalid` recipients and does not relay mail. Do not use these settings for a public deployment. The settings follow the [Mailpit SMTP documentation](https://mailpit.axllent.org/docs/configuration/smtp/) and [password file format](https://mailpit.axllent.org/docs/configuration/passwords/).

Example from the Factory checkout:

```sh
node tests/examples/completion/prepare-mail.mjs /absolute/private/umir-completion-mail
node scripts/completion/prepare.mjs \
  --directory /absolute/private/umir-completion-local \
  --inputs /absolute/private/umir-completion-mail/inputs.json \
  --project umir-completion-local \
  --origin http://127.0.0.1:18580 \
  --cookie-mode loopback-development \
  --sources /absolute/clean-completion-checkouts
COMPLETION_TEST_MAIL_DIR=/absolute/private/umir-completion-mail \
  docker compose --env-file /absolute/private/umir-completion-local/compose.env \
  -f compose.completion.yaml -f compose.completion.build.yaml \
  -f tests/examples/completion/compose.mail.yaml build
COMPLETION_TEST_MAIL_DIR=/absolute/private/umir-completion-mail \
  docker compose --env-file /absolute/private/umir-completion-local/compose.env \
  -f compose.completion.yaml -f tests/examples/completion/compose.mail.yaml \
  up -d --wait --wait-timeout 120
```

The source directory contains the five service directories named in `scripts/completion/sources.json`. Preparation verifies every clean exact commit before it enables the build override. Registry publication is a separate delivery step. The Mailpit UI uses loopback port 19125 only in this test recipe; the base assembly publishes only frontend.

Run the fixed seed explicitly after the application becomes healthy:

```sh
node tests/examples/completion/seed.mjs \
  --directory /absolute/private/umir-completion-local \
  --state-file /absolute/private/umir-completion-seed.json --demo
```

The state file must be outside the immutable configuration bundle. It records project, origin, actor, organization, epochs, the original timed delivery and command identities before mutations. The seed creates one organization/restaurant through Auth, five products, four dishes, one delivery and one order. It uses only supported public APIs. It checks low coverage plus one-portion deficit for cream, fish before parsley in recommendations, exclusion of six-month rice and an expired batch awaiting human writeoff.

A repeated complete seed in the same Warehouse epoch reads the existing result and sends no stock command. A partial seed resumes its saved payload and IDs; unrelated facts cause refusal. An unavailable owner is never empty. A fresh empty Warehouse epoch gets a fresh plan only if the previous order is terminal. An unknown old order remains unresolved and is never automatically sent to a new epoch. After a Serving-only reset, an already complete seed sends no order. Preserve the seed state file across restarts. A stale seed-only lock after a killed process requires checking that process before removing that lock; it is separate from Auth's writer lock and state.

No auto-seed occurs at container startup. `--demo`, the completion project guard and initial operator identity are required. This recipe is for a dedicated demonstration authority, not for filling existing customer data. Browser/runtime acceptance and the permitted public HTTPS readback remain separate from seed success. Retain the private bundle, Compose project name and Auth volume when stopping the stack. Do not delete volumes to restart it.
