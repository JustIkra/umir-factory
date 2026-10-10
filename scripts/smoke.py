import argparse
import json
import subprocess
from urllib.parse import urlsplit, urlunsplit
from urllib.request import urlopen


def published_url(compose, service):
    result = subprocess.run(
        [*compose, "port", service, "8080"],
        check=True, capture_output=True, text=True,
    )
    addresses = result.stdout.strip().splitlines()
    if not addresses:
        raise SystemExit(f"No published port for {service}; start the assembly first")
    address = urlsplit("http://" + addresses[0])
    host = address.hostname
    if not host or not address.port:
        raise SystemExit(f"Invalid published port for {service}")
    host = {"0.0.0.0": "127.0.0.1", "::": "::1"}.get(host, host)
    if ":" in host:
        host = f"[{host}]"
    return urlunsplit(("http", f"{host}:{address.port}", "", "", ""))


parser = argparse.ArgumentParser(description="Check the running Compose assembly")
parser.add_argument("--env-file", help="Compose environment file; otherwise Compose reads .env")
parser.add_argument("--web-url", help="Frontend base URL instead of its published Compose port")
parser.add_argument("--api-url", help="Gateway base URL instead of its published Compose port")
args = parser.parse_args()
compose = ["docker", "compose"]
if args.env_file:
    compose.extend(["--env-file", args.env_file])
web_url = (args.web_url or published_url(compose, "frontend")).rstrip("/")
api_url = (args.api_url or published_url(compose, "gateway")).rstrip("/")

for base_url in (web_url, api_url):
    for service in ("serving", "warehouse"):
        url = f"{base_url}/api/{service}/healthz"
        with urlopen(url, timeout=5) as response:
            payload = json.load(response)
            if response.status != 200 or payload != {"status": "ok", "service": f"umir-{service}"}:
                raise SystemExit(f"Unexpected response from {url}")
        print(f"OK: {base_url} -> gateway -> {service}")

with urlopen(f"{web_url}/", timeout=5) as response:
    if response.status != 200 or b'id="root"' not in response.read():
        raise SystemExit("Frontend entry point is unavailable")
print("OK: frontend entry point")
