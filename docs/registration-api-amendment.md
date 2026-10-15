# REG1: configurable public email registration

REG1 implements [factory issue 14](https://github.com/JustIkra/umir-factory/issues/14). It extends FIN1 through [public](api/registration-v1-public.json) and [private](api/registration-v1-private.json) schema overlays. The [composed public profile](api/registration-v1-composed-public.json), [composed private profile](api/registration-v1-composed-private.json), and [examples](api/registration-v1-examples.json) are its wire contract. C2, UA1 and FIN1 files retain their own meaning and evidence.

Generate with `node tests/http/tools/compose-registration-contract.mjs docs/api`. Run `make check-contract` for schema examples, negative values, reproducible composition, unchanged predecessor operations and private configuration preparation. The complete `make check` also runs the contract tests in the HTTP module.

## Enrollment

1. Obtain a preauth cookie and CSRF token through `GET /api/v2/csrf`.
2. Submit `POST /api/v2/email-challenges` with `{ commandID, email, purpose: "registration" }`.
3. Read the six-digit code from the existing registration email. Use the existing `POST /api/v2/challenges/{challengeID}/verify` with `{ commandID, challengeID, code }`. The existing resend operation is unchanged.
4. Submit `POST /api/v2/email-challenges/{challengeID}/complete` with `{ commandID, challengeID, proof, purpose: "registration", displayName, password, rememberMe? }`.
5. The gateway returns the existing `UserView` and installs the session cookie. Auth stores the user, confirmed email, consumed proof, session and command result in one commit. No organization or membership is granted. The user can call the existing `POST /api/v2/orgs` or accept an invitation.

`displayName` contains 2–80 code points after the existing canonicalization. `password` contains 8–32 code points. `newPassword` is forbidden for registration. Registration fields are forbidden for login and password reset. Ordinary password login keeps its current behavior and does not request an email code.

Email comparison retains the local part and lowercases the domain. Registration preserves the existing login identity rule: use the comparison email when it fits 128 code points; otherwise use the generated user ID returned in `user.login`. Clients use that returned login for password sign-in.

## Neutral responses and limits

Both new and occupied addresses receive `200 Challenge` with the same shape and deadlines. An occupied address has a private suppressed challenge and no OTP delivery. Suppressed issues consume the same address/global budgets. Neither begin, resend, verify nor command lookup exposes an existence flag. Codes for suppressed challenges cannot verify.

Existing limits apply: code lifetime 10 minutes, one minute between issues, at most four issues per challenge, three issues per address per 10 minutes, 60 issues globally per 10 minutes, five wrong codes then `ChallengeLocked`. The address budget includes invitation and other email jobs. Proof is bound to purpose, challenge and initiating preauth/session. Its existing retention period is 24 hours. A new command cannot consume a completed proof again.

Wrong, expired or consumed proofs produce `404 NotFound`. If another verified registration or invitation claimed the address before completion, completion produces `409 CommandConflict`. Invalid input produces `400 InvalidArgument`. Registration from an active session produces `403 Forbidden`. Existing challenge and throttle errors remain unchanged.

Exact command/body repeats recover the original result. A changed body with the same command returns `CommandConflict`. A lost reply does not create another user or session. Keep the original preauth cookie until completion is recovered; after receiving a session, authenticated registration is refused. Existing session expiry/revocation checks still apply to saved results.

## Configuration and persisted state

Auth reads `registration.enabled` from its private JSON configuration. The default is `false`. Set `{ "registration": { "enabled": true } }` in completion preparation inputs to opt in. The preparation tool accepts only a boolean and passes the value to Auth. The binding includes the full input digest, so changing registration policy requires a new reviewed bundle through the existing operational procedure.

When disabled, registration begin/complete and registration challenge verify/resend return `403 Forbidden`, including saved-command retries, without a command mutation. Invitation enrollment and other email purposes are unchanged. The flag alone does not change the selected assembly or deploy a new service version.

Auth upgrades valid schema v1/v2 state to v3 atomically before admission. The migration retains users, credentials, sessions, jobs and original command records. Schema v3 adds the registration email and suppression decision to challenges; it uses existing encrypted code/proof/outbox material. Old binaries must not open v3 state. Back up the state and its matched owner secrets before upgrade; restoring both loses subsequent changes and requires an explicit operational decision.

## Runtime acceptance

Use an isolated assembly with Nginx, the real Gateway/Auth production entrypoints and an authenticated STARTTLS SMTP receiver. Exercise begin → SMTP code → verify → complete, session cookie, password login and organization creation. Confirm occupied-address suppression, malformed input refusal and command replay. Schema checks alone do not prove mail delivery or runtime acceptance. Source branches and their acceptance evidence do not change deployment pins automatically.

Run the reusable acceptance client against that isolated assembly:

```sh
node tests/examples/registration/acceptance.mjs \
  --origin http://127.0.0.1:19580 \
  --mailpit http://127.0.0.1:19525 \
  --output /absolute/private/registration-receipt.json
```

The client generates a fresh `.invalid` address and password in memory, reads its code only through the SMTP receiver, checks replay and suppression, and writes a receipt without cookies, passwords or OTPs. It creates one user and organization in the supplied test assembly. Use a disposable assembly owned by this acceptance run.
