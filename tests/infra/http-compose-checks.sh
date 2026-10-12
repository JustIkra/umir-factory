#!/usr/bin/env bash
set -euo pipefail

factory_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$factory_dir"
GOWORK=off go -C tools/assembly-checks test -tags composeintegration -run '^(TestHTTPComposeOverridesResolveAsIsolatedTopologies|TestAuthCandidateOverridePublishesOnlyTheFrontend)$' -count=1
