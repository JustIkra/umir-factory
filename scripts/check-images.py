import json
import re
import subprocess

result = subprocess.run(
    ["docker", "compose", "--env-file", ".env.example", "config", "--format", "json"],
    check=True, capture_output=True, text=True,
)
services = json.loads(result.stdout)["services"]
expected = {"gateway", "serving", "warehouse", "frontend"}
if set(services) != expected:
    raise SystemExit("Unexpected assembly services")
for name in sorted(expected):
    image = services[name]["image"]
    if not re.fullmatch(rf"ghcr\.io/justikra/umir-{name}:sha-[0-9a-f]{{40}}", image):
        raise SystemExit(f"Invalid pinned image for {name}")
print("All service images use full commit SHA tags")

