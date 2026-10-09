import json
from urllib.request import urlopen

for port in (8080, 8081):
    for service in ("serving", "warehouse"):
        url = f"http://127.0.0.1:{port}/api/{service}/healthz"
        with urlopen(url, timeout=5) as response:
            payload = json.load(response)
            if response.status != 200 or payload != {"status": "ok", "service": f"umir-{service}"}:
                raise SystemExit(f"Unexpected response from {url}")
        print(f"OK: port {port} -> gateway -> {service}")

with urlopen("http://127.0.0.1:8080/", timeout=5) as response:
    if response.status != 200 or b'id="root"' not in response.read():
        raise SystemExit("Frontend entry point is unavailable")
print("OK: frontend entry point")
