import assert from "node:assert/strict";
import { test } from "node:test";
import {
  chooseEpoch,
  makeDemoPlan,
  createPublicClient,
} from "../../examples/completion/seed.mjs";

test("fresh seeding requires a known empty prefix and refuses unavailable or unrelated nonempty data", () => {
  assert.equal(
    chooseEpoch(undefined, {
      epoch: "new",
      stateVersion: "0",
      products: 0,
      dishes: 0,
    }),
    "fresh",
  );
  for (const current of [
    { epoch: "new", stateVersion: "1", products: 0, dishes: 0 },
    { epoch: "new", stateVersion: "0", products: 1, dishes: 0 },
    undefined,
  ])
    assert.throws(() => chooseEpoch(undefined, current));
});
test("same-epoch partial seed resumes, completed seed is read-only, reset never redelivers an unknown old order", () => {
  const current = { epoch: "same", stateVersion: "10", products: 5, dishes: 4 };
  assert.equal(
    chooseEpoch({ epoch: "same", status: "partial" }, current),
    "resume",
  );
  assert.equal(
    chooseEpoch({ epoch: "same", status: "complete" }, current),
    "already-seeded",
  );
  assert.throws(() =>
    chooseEpoch(
      { epoch: "old", status: "partial", orderAttempt: { status: "unknown" } },
      { ...current, epoch: "new", stateVersion: "0", products: 0, dishes: 0 },
    ),
  );
  assert.equal(
    chooseEpoch(
      { epoch: "old", status: "complete" },
      { ...current, epoch: "new", stateVersion: "0", products: 0, dishes: 0 },
    ),
    "fresh",
  );
});
test("demo times and quantities remain fixed for a captured epoch plan", () => {
  const plan = makeDemoPlan("2026-10-15T12:00:00+03:00");
  assert.equal(
    plan.delivery.lines[0].manufacturedAt.instant,
    "2026-10-14T12:00:00+03:00",
  );
  assert.equal(plan.delivery.lines[0].quantity, "100000");
  assert.equal(
    plan.delivery.lines[4].receivedAt.instant,
    "2026-10-13T13:00:00+03:00",
  );
  assert.deepEqual(plan.order, [
    { dishID: "FIN-FISH-RICE", portions: "1" },
    { dishID: "FIN-CREAM-DISH", portions: "2" },
  ]);
});

test("public seed client removes the cleared preauth cookie when the real session is issued", async () => {
  const client = createPublicClient(
    "http://127.0.0.1:18580",
    async (url, request) => {
      const path = new URL(url).pathname;
      if (path === "/api/v2/csrf")
        return new Response(JSON.stringify({ csrfToken: "preauth-csrf" }), {
          headers: { "Set-Cookie": "umir_operator_preauth=temporary; Path=/" },
        });
      if (path === "/api/v2/session") {
        assert.equal(request.headers.Cookie, "umir_operator_preauth=temporary");
        assert.equal(request.headers["X-CSRF-Token"], "preauth-csrf");
        const headers = new Headers();
        headers.append("Set-Cookie", "umir_operator_session=retained; Path=/");
        headers.append(
          "Set-Cookie",
          "umir_operator_preauth=; Path=/; Max-Age=0",
        );
        return new Response(
          JSON.stringify({
            user: { userID: "operator" },
            session: { csrfToken: "session-csrf" },
          }),
          { headers },
        );
      }
      assert.equal(request.headers.Cookie, "umir_operator_session=retained");
      return new Response(JSON.stringify({ organizations: [] }));
    },
  );
  await client.login(
    { initialUser: { login: "operator" } },
    "synthetic-private-value",
  );
  assert.deepEqual(await client.request("GET", "/api/v2/access"), {
    organizations: [],
  });
});
