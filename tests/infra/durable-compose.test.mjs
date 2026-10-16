import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const factory = new URL("../../", import.meta.url).pathname;
const compose = new URL("../../compose.durable.yaml", import.meta.url).pathname;
const profile = readFileSync(compose, "utf8");
const lock = JSON.parse(readFileSync(new URL("../../scripts/durable/images.lock.json", import.meta.url)));

function model() {
  return JSON.parse(
    execFileSync("docker", ["compose", "-f", compose, "config", "--format", "json"], {
      cwd: factory,
      encoding: "utf8",
      timeout: 20000,
      env: {
        ...process.env,
        DURABLE_PROJECT: "umir-durable-compose-test",
        DURABLE_STATE_DIR: "/tmp/umir-durable-compose-test",
        DURABLE_POSTGRES_PORT: "55432",
        DURABLE_RABBITMQ_PORT: "55672",
        DURABLE_RABBITMQ_MANAGEMENT_PORT: "55673",
        DURABLE_REDIS_LIMITER_PORT: "56379",
        DURABLE_REDIS_CACHE_PORT: "56380",
        DURABLE_DEFAULT_SUBNET: "10.249.0.0/24",
        DURABLE_DATA_SUBNET: "10.249.1.0/24",
        DURABLE_FRONT_SUBNET: "10.249.2.0/24",
        DURABLE_MAIL_SUBNET: "10.249.3.0/24",
        DURABLE_EDGE_SUBNET: "10.249.4.0/24",
        DURABLE_RABBITMQ_MANAGEMENT_PORT: "55673",
      },
    }),
  );
}

test("durable profile has fresh state and internal storage networks", () => {
  assert.match(profile, /x-umir-durable-profile:\s*DUR1/);
  assert.doesNotMatch(profile, /auth-state|warehouse-data|serving-data|feedback-data/);
  const result = model();
  assert.deepEqual(
    Object.keys(result.volumes).sort(),
    ["auth-owner-secrets", "mq-data", "pg-data", "redis-limiter-data", "umir-secrets"].sort(),
  );
  assert.equal(result.services["redis-cache"].volumes.some((volume) => volume.target === "/data"), false);
  assert.equal(result.services["redis-limiter"].user, "999:999");
  assert.equal(result.services["redis-cache"].user, "999:999");
  assert.equal(result.networks.data.internal, true);
  assert.ok(Object.hasOwn(result.services.postgres.networks, "data"));
});

test("durable infrastructure pins supported images and separates Redis policies", () => {
  const result = model();
  for (const service of ["postgres", "rabbitmq", "redis-limiter", "redis-cache"]) {
    assert.match(result.services[service].image, /@sha256:[a-f0-9]{64}$/);
    assert.ok(result.services[service].mem_limit);
  }
  assert.match(result.services.postgres.image, /postgres:17/);
  assert.match(result.services.rabbitmq.image, /rabbitmq:4/);
  assert.match(result.services["redis-limiter"].command.join(" "), /--appendfsync everysec/);
  assert.match(result.services["redis-limiter"].command.join(" "), /--maxmemory-policy noeviction/);
  assert.match(result.services["redis-cache"].command.join(" "), /--maxmemory 512mb/);
  assert.match(result.services["redis-cache"].command.join(" "), /--maxmemory-policy allkeys-lru/);
  assert.equal(result.services["redis-cache"].volumes.some((volume) => volume.target === "/data"), false);
  for (const [service, lockedName] of Object.entries({ postgres: "postgres", rabbitmq: "rabbitmq", "redis-limiter": "redis", "redis-cache": "redis" })) {
    const [registry, index] = result.services[service].image.split("@");
    assert.equal(registry, lock.images[lockedName].ref);
    assert.equal(index, lock.images[lockedName].index);
  }
});

test("init jobs are isolated and secret values never enter service environments", () => {
  const result = model();
  for (const job of ["secrets-init", "pg-roles", "mq-init", "redis-init"]) {
    assert.ok(result.services[job], `missing ${job}`);
    assert.equal(result.services[job].restart, "no");
  }
  assert.equal(result.services.postgres.environment?.POSTGRES_PASSWORD, undefined);
  assert.equal(result.services.rabbitmq.environment?.RABBITMQ_DEFAULT_PASS, undefined);
  for (const [serviceName, service] of Object.entries(result.services)) {
    for (const [key, value] of Object.entries(service.environment ?? {})) {
      if (/PASSWORD|SECRET|TOKEN|_URL$|_KEY$/.test(key)) {
        assert.match(key, /_FILE$/, `${serviceName}.${key} must reference a file`);
        assert.match(value, /^\//, `${serviceName}.${key} must be a path`);
      }
      assert.doesNotMatch(String(value), /(?:postgres|amqps?|rediss?):\/\//i, `${serviceName}.${key} contains a URL`);
    }
  }
});

test("only infrastructure init owns topology and service ports stay private", () => {
  const result = model();
  assert.deepEqual(result.services.postgres.ports, undefined);
  assert.deepEqual(result.services["redis-limiter"].ports, undefined);
  assert.deepEqual(result.services["redis-cache"].ports, undefined);
  assert.deepEqual(result.services.rabbitmq.ports, undefined);
  assert.ok(result.services["mq-init"].depends_on.rabbitmq);
  assert.deepEqual(result.services["mq-init"].command, ["/var/lib/rabbitmq/runtime/definitions.json"]);
  assert.deepEqual(result.services["mq-init"].entrypoint, ["/bin/sh", "/scripts/mq-init.sh"]);
  assert.equal(result.services["mq-init"].image, result.services.rabbitmq.image);
});

test("RabbitMQ management is published only by the loopback ops overlay", () => {
  const opsCompose = new URL("../../compose.durable.ops.yaml", import.meta.url).pathname;
  const result = JSON.parse(
    execFileSync("docker", ["compose", "-f", compose, "-f", opsCompose, "config", "--format", "json"], {
      cwd: factory,
      encoding: "utf8",
      timeout: 20000,
      env: { ...process.env, DURABLE_PROJECT: "umir-durable-ops-test", DURABLE_DEFAULT_SUBNET: "10.248.0.0/24", DURABLE_DATA_SUBNET: "10.248.1.0/24", DURABLE_FRONT_SUBNET: "10.248.2.0/24", DURABLE_MAIL_SUBNET: "10.248.3.0/24", DURABLE_EDGE_SUBNET: "10.248.4.0/24", DURABLE_RABBITMQ_MANAGEMENT_PORT: "55673" },
    }),
  );
  assert.equal(result.services.rabbitmq.ports[0].host_ip, "127.0.0.1");
  assert.equal(result.services.rabbitmq.ports[0].published, "55673");
});
