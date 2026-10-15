import { createHash, randomUUID } from "node:crypto";
import {
  closeSync,
  existsSync,
  fsyncSync,
  lstatSync,
  openSync,
  readFileSync,
  renameSync,
  unlinkSync,
  writeSync,
} from "node:fs";
import { dirname, isAbsolute, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const data = JSON.parse(
  readFileSync(new URL("./data.json", import.meta.url), "utf8"),
);
const hash = (value) => createHash("sha256").update(value).digest("hex");
const moscow = (ms) =>
  new Date(ms + 3 * 3600000)
    .toISOString()
    .replace(".000Z", "+03:00")
    .replace("Z", "+03:00");
export function chooseEpoch(previous, current) {
  if (
    current === undefined ||
    typeof current.epoch !== "string" ||
    !/^\d+$/.test(current.stateVersion) ||
    !Number.isInteger(current.products) ||
    !Number.isInteger(current.dishes)
  )
    throw new Error("seed requires known available owner state");
  if (previous?.epoch === current.epoch)
    return previous.status === "complete" ? "already-seeded" : "resume";
  if (
    previous !== undefined &&
    previous.orderAttempt !== undefined &&
    previous.orderAttempt.status !== "committed"
  )
    throw new Error(
      "an old unknown order cannot be redelivered after an epoch change",
    );
  if (
    current.stateVersion !== "0" ||
    current.products !== 0 ||
    current.dishes !== 0
  )
    throw new Error("fresh seed requires an empty demo epoch");
  return "fresh";
}
export function makeDemoPlan(at) {
  const now = Date.parse(at);
  if (!Number.isFinite(now)) throw new Error("invalid captured seed instant");
  return {
    version: data.version,
    products: data.products,
    dishes: data.dishes,
    delivery: {
      number: "FIN1-demo",
      createdAt: { instant: moscow(now) },
      lines: data.batches.map(
        ({ manufacturedHoursAgo, receivedHoursAgo, ...line }) => ({
          ...line,
          manufacturedAt: {
            instant: moscow(now - manufacturedHoursAgo * 3600000),
          },
          receivedAt: { instant: moscow(now - receivedHoursAgo * 3600000) },
        }),
      ),
    },
    order: data.order,
  };
}
function privateRead(path) {
  if (!isAbsolute(path))
    throw new Error("seed inputs must use absolute private paths");
  const stat = lstatSync(path);
  if (
    !stat.isFile() ||
    stat.isSymbolicLink() ||
    stat.nlink !== 1 ||
    (stat.mode & 0o777) !== 0o600 ||
    stat.uid !== process.getuid() ||
    stat.size > 1048576
  )
    throw new Error("seed requires owned regular 0600 input files");
  return readFileSync(path, "utf8");
}
function save(path, value) {
  const temporary = `${path}.${randomUUID()}.tmp`;
  const file = openSync(temporary, "wx", 0o600);
  try {
    writeSync(file, JSON.stringify(value, null, 2) + "\n");
    fsyncSync(file);
  } finally {
    closeSync(file);
  }
  renameSync(temporary, path);
  const parent = openSync(dirname(path), "r");
  try {
    fsyncSync(parent);
  } finally {
    closeSync(parent);
  }
}
export function createPublicClient(origin, transport = fetch) {
  const cookies = new Map();
  let csrf;
  async function request(method, path, body, epoch) {
    let response;
    try {
      response = await transport(origin + path, {
        method,
        redirect: "manual",
        signal: AbortSignal.timeout(10000),
        headers: {
          Accept: "application/json",
          Origin: origin,
          ...(cookies.size
            ? { Cookie: [...cookies].map(([k, v]) => `${k}=${v}`).join("; ") }
            : {}),
          ...(body === undefined ? {} : { "Content-Type": "application/json" }),
          ...(method === "GET" || csrf === undefined
            ? {}
            : { "X-CSRF-Token": csrf }),
          ...(epoch === undefined ? {} : { "X-Umir-Runtime-Epoch": epoch }),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
    } catch {
      throw new Error(
        `public ${method} ${path.split("?")[0]} has unknown transport outcome; retry the saved seed only`,
      );
    }
    for (const cookie of response.headers.getSetCookie()) {
      const pair = cookie.split(";", 1)[0];
      const separator = pair.indexOf("=");
      const name = pair.slice(0, separator);
      const value = pair.slice(separator + 1);
      if (value === "" || /;\s*Max-Age=0(?:;|$)/i.test(cookie))
        cookies.delete(name);
      else cookies.set(name, value);
    }
    let value;
    try {
      value = await response.json();
    } catch {
      throw new Error(
        `public ${method} returned non-JSON status ${response.status}`,
      );
    }
    if (!response.ok)
      throw new Error(
        `public ${method} ${path.split("?")[0]} refused with HTTP ${response.status}, code ${value.code ?? value.error?.code ?? "unavailable"}, outcome ${value.outcome ?? value.error?.outcome ?? "unknown"}`,
      );
    if (typeof value.csrfToken === "string") csrf = value.csrfToken;
    if (typeof value.session?.csrfToken === "string")
      csrf = value.session.csrfToken;
    return value;
  }
  return {
    request,
    async login(config, password) {
      await request("GET", "/api/v2/csrf");
      return request("POST", "/api/v2/session", {
        commandID: randomUUID(),
        login: config.initialUser.login,
        password,
        rememberMe: false,
      });
    },
  };
}

export async function seed(
  { directory, stateFile, demo = false },
  transport = fetch,
) {
  directory = resolve(directory);
  stateFile = resolve(stateFile);
  const bundle = JSON.parse(privateRead(directory + "/bundle.json"));
  const { project, origin } = bundle.binding;
  if (
    !demo ||
    !/^umir-completion-[a-z0-9-]+$/.test(project) ||
    stateFile.startsWith(directory + "/")
  )
    throw new Error(
      "explicit --demo and an isolated completion project with a separate seed state file are required",
    );
  const config = JSON.parse(privateRead(directory + "/auth/config.json"));
  const password = privateRead(directory + "/auth/secrets/password");
  const binding = { project, origin, userID: config.initialUser.userID };
  const lock = openSync(stateFile + ".lock", "wx", 0o600);
  try {
    writeSync(lock, JSON.stringify({ pid: process.pid, project }) + "\n");
    let state = existsSync(stateFile)
      ? JSON.parse(privateRead(stateFile))
      : {
          version: 1,
          binding,
          organizationCommand: randomUUID(),
          createStarted: false,
        };
    if (
      state.version !== 1 ||
      JSON.stringify(state.binding) !== JSON.stringify(binding)
    )
      throw new Error("seed state scope differs from the private demo bundle");
    const persist = () => save(stateFile, state);
    persist();
    const client = createPublicClient(origin, transport);
    const session = await client.login(config, password);
    if (session.user?.userID !== binding.userID)
      throw new Error("seed actor differs from the retained initial operator");
    const api = client.request;
    let access = await api("GET", "/api/v2/access");
    if (state.orgID === undefined) {
      if (
        access.organizations.length !== 0 &&
        (!state.createStarted ||
          access.organizations.length !== 1 ||
          access.organizations[0].name !== data.organization ||
          access.restaurants.length !== 1 ||
          access.restaurants[0].name !== data.restaurant)
      )
        throw new Error(
          "demo organization creation requires empty or recorded partial authority",
        );
      state.createStarted = true;
      persist();
      const result = await api("POST", "/api/v2/orgs", {
        commandID: state.organizationCommand,
        name: data.organization,
        timeZone: "Europe/Moscow",
        firstRestaurantName: data.restaurant,
      });
      if (
        typeof result.orgID !== "string" ||
        result.restaurantIDs?.length !== 1
      )
        throw new Error("unexpected organization creation result");
      state.orgID = result.orgID;
      state.restaurantID = result.restaurantIDs[0];
      persist();
    }
    for (let count = 0; count < 60; count++) {
      access = await api("GET", "/api/v2/access");
      const organization = access.organizations.find(
        (v) => v.orgID === state.orgID,
      );
      const restaurant = access.restaurants.find(
        (v) => v.restaurantID === state.restaurantID,
      );
      if (
        organization?.name !== data.organization ||
        restaurant?.orgID !== state.orgID ||
        restaurant.name !== data.restaurant
      )
        throw new Error("recorded demo scope is absent or changed");
      if (restaurant.lifecycle === "ready") break;
      if (count === 59) throw new Error("demo peers did not become ready");
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
    const root = `/api/v2/r/${state.restaurantID}`;
    const [products, dishes, menu] = await Promise.all([
      api("GET", root + "/warehouse/products"),
      api("GET", root + "/warehouse/dish-catalog"),
      api("GET", root + "/serving/menu"),
    ]);
    const current = {
      epoch: products.context.runtimeEpoch,
      stateVersion: products.context.stateVersion,
      products: products.result.items.length,
      dishes: dishes.result.items.length,
    };
    if (dishes.context.runtimeEpoch !== current.epoch)
      throw new Error("owner epoch changed while inspecting seed state");
    const action = chooseEpoch(state.current, current);
    if (action === "already-seeded")
      return {
        project,
        orgID: state.orgID,
        restaurantID: state.restaurantID,
        epoch: current.epoch,
        status: action,
        orderID: state.current.orderAttempt?.orderID,
      };
    if (action === "fresh") {
      state.previous = state.current;
      state.current = {
        epoch: current.epoch,
        status: "partial",
        plan: makeDemoPlan(products.context.evaluatedAt),
        settingsVersion: products.context.settings.settingsVersion,
        completed: [],
        servingEpoch: menu.context.runtimeEpoch,
      };
      persist();
    }
    const active = state.current;
    const plan = active.plan;
    if (
      plan.version !== data.version ||
      menu.context.warehouseEpoch !== active.epoch ||
      menu.context.runtimeEpoch !== active.servingEpoch
    )
      throw new Error(
        "seed plan or saved runtime scope changed; old attempts are retained",
      );
    const prefix = "fin1-seed-" + hash(active.epoch).slice(0, 16);
    const command = (name) => `${prefix}-${name}`;
    const known = new Set([
      ...plan.products.map((_, i) => command(`product-${i}`)),
      ...plan.dishes.map((_, i) => command(`dish-${i}`)),
      command("delivery"),
    ]);
    const history = await api("GET", root + "/warehouse/history");
    if (
      history.context.runtimeEpoch !== active.epoch ||
      history.result.events.length > 500 ||
      BigInt(history.context.stateVersion) > 500n
    )
      throw new Error(
        "partial seed history is unavailable or beyond its fixed demo bounds",
      );
    for (const { event } of history.result.events) {
      if (
        !known.has(event.commandID) &&
        event.type !== "BatchExpiryPending" &&
        !(
          event.type === "OrderPlaced" &&
          event.payload.order.proposalID === active.orderAttempt?.proposalID
        )
      )
        throw new Error("unrelated mutations found in a partial demo seed");
    }
    async function writeStep(name, path, payload) {
      if (active.completed.includes(name)) return;
      active.inFlight = name;
      persist();
      const result = await api(
        "POST",
        root + path,
        { ...payload, commandID: command(name) },
        active.epoch,
      );
      if (
        result.context?.runtimeEpoch !== active.epoch ||
        result.context.tenantID !== state.restaurantID
      )
        throw new Error("seed write result differs from saved scope");
      active.completed.push(name);
      delete active.inFlight;
      persist();
    }
    for (const [i, product] of plan.products.entries())
      await writeStep(`product-${i}`, "/warehouse/products", product);
    for (const [i, dish] of plan.dishes.entries())
      await writeStep(`dish-${i}`, "/warehouse/dishes", dish);
    await writeStep("delivery", "/warehouse/deliveries", {
      ...plan.delivery,
      deliveryID: prefix + "-delivery",
      expectedSettingsVersion: active.settingsVersion,
    });
    if (active.orderAttempt?.status !== "committed") {
      if (active.orderAttempt === undefined) {
        const proposal = await api(
          "POST",
          root + "/serving/order-proposals",
          { positions: plan.order },
          active.servingEpoch,
        );
        if (
          proposal.context.runtimeEpoch !== active.servingEpoch ||
          proposal.context.warehouseEpoch !== active.epoch ||
          proposal.result.changes.length !== 0 ||
          proposal.result.acceptedPositions.length !== plan.order.length
        )
          throw new Error("demo order was not fully accepted");
        active.orderAttempt = {
          proposalID: proposal.result.proposalID,
          commandID: command("confirm"),
          status: "unknown",
        };
        persist();
      }
      const attempt = active.orderAttempt;
      const confirmed = await api(
        "POST",
        root + "/serving/order-confirmations",
        { commandID: attempt.commandID, proposalID: attempt.proposalID },
        active.servingEpoch,
      );
      if (confirmed.result.kind !== "OrderConfirmed")
        throw new Error(
          `demo confirmation is ${confirmed.result.kind ?? "unknown"}; saved attempt retained`,
        );
      attempt.status = "committed";
      attempt.orderID = confirmed.result.order.orderID;
      persist();
    }
    const [supply, recommendations, pending] = await Promise.all([
      api("GET", root + "/warehouse/supply-suggestions"),
      api("GET", root + "/serving/recommendations"),
      api("GET", root + "/warehouse/pending-writeoffs"),
    ]);
    const cream = supply.result.items.find((v) => v.productID === "FIN-CREAM");
    if (
      cream?.suggestedQuantity !== "15000" ||
      !cream.reasons.includes("low_coverage") ||
      !cream.reasons.includes("stop_list_deficit") ||
      recommendations.result.items[0]?.dishID !== "FIN-FISH-RICE" ||
      recommendations.result.items.some((v) => v.dishID === "FIN-RICE-DISH") ||
      !pending.items.some((v) => v.batch.productID === "FIN-EXPIRED")
    )
      throw new Error(
        "seeded FIN1 facts differ from the expected demo; no additional order will be sent",
      );
    active.status = "complete";
    persist();
    return {
      project,
      orgID: state.orgID,
      restaurantID: state.restaurantID,
      epoch: active.epoch,
      status: "seeded",
      orderID: active.orderAttempt.orderID,
      supply: cream.suggestedQuantity,
      recommendations: recommendations.result.items.map((v) => v.dishID),
    };
  } finally {
    closeSync(lock);
    unlinkSync(stateFile + ".lock");
  }
}

if (
  process.argv[1] !== undefined &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    const options = {};
    const names = new Map([
      ["--directory", "directory"],
      ["--state-file", "stateFile"],
    ]);
    for (let i = 2; i < process.argv.length; i++) {
      if (process.argv[i] === "--demo") {
        options.demo = true;
        continue;
      }
      const key = names.get(process.argv[i]);
      const value = process.argv[++i];
      if (key === undefined || value === undefined || key in options)
        throw new Error("invalid seed argument");
      options[key] = value;
    }
    console.log(JSON.stringify(await seed(options)));
  } catch (error) {
    console.error(error instanceof Error ? error.message : "demo seed failed");
    process.exitCode = 1;
  }
}
