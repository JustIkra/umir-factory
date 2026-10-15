import { readFileSync } from "node:fs";
import { get } from "node:https";
const request = get(
  "https://auth:8085/internal/auth/v1/jwks",
  {
    ca: readFileSync("/run/auth-health/ca.crt"),
    headers: {
      "X-Umir-Service-Key": readFileSync(
        "/run/auth-health/service-key",
        "utf8",
      ),
    },
    timeout: 2000,
  },
  (response) => {
    response.resume();
    response.on("end", () => process.exit(response.statusCode === 200 ? 0 : 1));
  },
);
request.on("error", () => process.exit(1));
request.on("timeout", () => {
  request.destroy();
  process.exit(1);
});
