# Backend candidate runner

The `http-acceptance --backend-only` mode builds and runs Auth, Gateway,
Warehouse, Serving and Feedback. It uses the frozen C2 contract
`2.0.0-draft.2`, identity
`sha256:733e102ff0b4f52bb49d435716f9f1720642c8fad3a8a5d0685847071950e9e6`.

This mode has a separate topology. Existing selected, four-service candidate,
operator-auth and realtime modes keep their current commands and contracts.
The backend runner needs a compiled F3 harness and a reviewed operational input
file. F2 implements orchestration and tests it with command doubles. Real
service compatibility and API acceptance are J1/E gates.

## Invocation

Build from this nested module with Go 1.26.9 and `GOWORK=off`:

```sh
go build -o /tmp/umir-checks .
```

Use absolute paths. Obtain the source refs, input digest and harness digest from
reviewed evidence. Run from any directory:

```sh
/tmp/umir-checks http-acceptance \
  --factory-dir "$FACTORY_CANDIDATE" \
  --candidate --backend-only \
  --auth-ref "$AUTH_SHA" \
  --gateway-ref "$GATEWAY_SHA" \
  --warehouse-ref "$WAREHOUSE_SHA" \
  --serving-ref "$SERVING_SHA" \
  --feedback-ref "$FEEDBACK_SHA" \
  --backend-inputs "$REVIEWED_INPUT_FILE" \
  --backend-inputs-sha256 "$INPUT_SHA256" \
  --backend-harness "$F3_HARNESS_BINARY" \
  --backend-harness-sha256 "$HARNESS_SHA256" \
  --backend-evidence-dir "$GROUP_EVIDENCE_DIRECTORY"
```

Add `--reviewed-dirty` only when the reviewed input declares at least one
`reviewed-dirty` source. Clean services may coexist with reviewed dirty services.
All refs are full lowercase 40-character commit SHAs. Digests are lowercase
64-character SHA-256 values. This mode rejects `--selected`, `--auth`,
`--realtime` and `--frontend-ref`.

## Reviewed operational input

The JSON input has these top-level fields. Unknown or duplicate fields, trailing
JSON values, excessive nesting, and input larger than 16 MiB fail validation.

| Field | Value |
| --- | --- |
| `version` | Integer `1` |
| `contractIdentity` | The frozen C2 identity above |
| `services` | Exactly `auth`, `gateway`, `warehouse`, `serving`, `feedback` |
| `sinks` | Exactly `smtp` and `notification` |
| `requirements` | Nonempty unique acceptance assertion IDs assigned to this invocation |

Every service entry has these fields:

| Field | Meaning |
| --- | --- |
| `path` | Absolute local Git checkout directory |
| `ref` | Exact HEAD, equal to its command-line ref |
| `sourceMode` | `clean` or `reviewed-dirty` |
| `files` | Complete existing tracked and nonignored untracked working-tree file manifest |
| `dockerfile` | Relative Dockerfile path present in `files` |
| `port` | Primary private listener container port, integer 1–65535 |
| `publicPort` | Required only for Gateway; its browser API container port |
| `statePath` | Required only for Auth/Feedback; container directory under `/var/lib/` |
| `environment` | Map of environment names to literal values or complete templates |
| `command` | Optional exec-form command override; an empty array uses the image command |
| `healthcheck` | Exec-form readiness check beginning with `CMD` |

A manifest file has `path`, `sha256` and `mode`. Paths are relative, unique and
normalized. Mode is decimal `420` for 0644 or `493` for 0755. Symlinks, Git
metadata and entries that escape the checkout are rejected. The source origin
must be its `JustIkra/umir-<service>` repository, using HTTPS or SSH. The copied
`go.mod` must declare that service's module.

The runner verifies HEAD, clean status when required, the exact file inventory,
file modes and all hashes. It builds from a private copy of those bytes.
Reviewed dirty snapshots accept tracked deletions with or without staging. Omit
deleted files from the manifest. Index staging does not change the copied content.
Ignored untracked paths are recorded as excluded and are never copied. Include
tracked `.dockerignore` and Dockerfile-specific ignore files in the manifest;
Docker applies those reviewed rules to the copied context. The context digest
covers the sorted complete prefilter manifest, including those ignore files.
No source checkout is used as a live Docker context or mounted as runtime state.

A sink entry has `image`, `port`, `controlPort`, and an optional `command` array.
Its image must use an exact `@sha256:` digest. `port` is the delivery listener;
`controlPort` is the loopback-published observation/fault-control listener.
J1 must review the chosen images as synthetic sinks and verify their APIs.
The runner does not choose or publish sink images.

Service and sink commands and service readiness checks are literal argument
arrays. The runner escapes dollar signs in generated Compose values to prevent
host environment interpolation. A shell invoked explicitly by an argument array
can still expand its container environment. Canonical `docker compose config`
output doubles literal dollar signs for serialization.

Operational names, startup commands, bootstrap configuration, state paths and
readiness checks must match the reviewed service owners' implementations. They
are explicit inputs because the wire contract does not define process flags or
environment variable names. J1 binds these inputs to the final service revisions.
F2 does not guess those names or wait for unfinished peer source.

## Environment and isolation

Templates must occupy the complete value:

| Template | Expansion |
| --- | --- |
| `{{origin}}` | Gateway's captured loopback HTTP origin |
| `{{url:<service>}}` | Private HTTP URL for one of the five services or the two sinks |
| `{{secret:<name>}}` | A fresh synthetic secret belonging to this run |

Supported secret names are `gateway-auth`, `auth-warehouse`, `auth-serving`,
`auth-gateway`, `gateway-warehouse`, `gateway-serving`, `gateway-feedback`,
`serving-warehouse`, and `bootstrap-password`. Only named endpoints may receive
a connection's secret. `bootstrap-password` is restricted to Auth and the private
harness descriptor. Auth owns its signing key. Gateway receives no signing key.
Use a reviewed private bootstrap mechanism to derive the supplied initial
verifier/profile; there is no default production password or public signup.

Credential environment names require secret templates. Literal HTTP URLs may
name only the seven declared services. Recipient addresses must use `.invalid`
domains. Newlines, dollar interpolation and embedded templates are rejected.
The generated private environment files have mode 0600. Application containers
receive only the declared environment and their image defaults.

Every run has a random `umir-backend-<24 hex>` Compose project, two project-owned
named volumes, and an internal network with no external egress. Version 1 publishes
only Gateway and sink control listeners on loopback. Version 2 also publishes the
five private business HTTP listeners on ephemeral loopback ports. Gateway uses a kernel-selected
unused ephemeral port captured before start, so configured Origin remains exact.
A bind race fails the run and triggers cleanup. Compose assigns sink ports.
Auth and Feedback have separate durable volumes. Restart or business-memory
reset does not delete either volume. Warehouse and Serving get no durable mount.
No host Auth files or production volumes are mounted. Run groups need separate
input/clock fixtures and separate invocations; never share one mutable stack.

## Private business probes in backend-run version 2

Version 2 adds the required `privateServiceURLs` object to the private run
descriptor. It has exactly `auth`, `warehouse`, `serving`, `feedback`, and
`gateway`. Each value is `http://127.0.0.1:<port>` with a canonical decimal port
from 1 through 65535. Paths, user information, queries, fragments, other schemes,
other hosts, zero ports and duplicate endpoints are rejected. The five URLs must
also differ from the public Gateway and both sink control URLs.

The generated test topology maps Auth, Warehouse, Serving and Feedback container
port 8080, and Gateway private port 8082, with `host_ip=127.0.0.1` and
`published="0"`. Gateway public port 8080 keeps its separate captured Origin.
The ports come from the reviewed version 2 launch bindings. Unix fixture controls
remain unexposed. Production Compose and version 1 topology are unchanged.

After startup and owner readiness, the runner discovers each endpoint with
`docker compose --project-name <project> --file <compose> port <service> <private-port>`.
It verifies the full owned inventory before each lookup and after discovery.
Project, service, run label, exact container ID, reviewed image ID, ready state,
target port, unique host port and TCP loopback mapping must match. A replaced
container or differing mapping fails before the harness runs. Discovery has a
60-second total deadline; each Docker read has a 10-second deadline and 256 KiB
output bound. Endpoint output has a 128-byte bound.

These URLs appear only in the temporary mode-0600 descriptor and the private
`evidence.json` allowlist. They are absent from public observations and command
output. Existing project cleanup removes all mappings with the owned containers,
volumes and network, and deletes the private workspace. Foreign or unverifiable
ownership stops project deletion and records cleanup failure.

This is an unpublished F2/F3 test integration amendment. It does not change C2,
RPC 1.1.0, or binding digest
`af215b378140b8e690523fc537007679b8c2599ed3039f7f2a0c651a64c59410`.
The new contract, strict schema and examples are in the task evidence directory
`.memory-base/plans/backend-e2e/runs/backend-e2e-20261012/component-fixtures/factory-runner/private-probe-routing/implement`
under the workspace root. Approved normative snapshots remain immutable.

The F3 integrator must extend its strict version 2 decoder and endpoint helper
after review of this amendment. It must revalidate current project/container/
image/target mappings before probes and after service restart, reject redirects,
bound request/response sizes and deadlines, and keep these URLs in private
metadata/evidence. Restart uses the same Compose project and file; a container
recreation requires fresh ownership and mapping checks before any further probe.
Legitimate service credentials come from the existing harness file references;
legitimate tokens come from real Auth interfaces. The harness never signs tokens.
Malformed values are used only for rejection tests. Old version 2 consumers
reject the additional field until integrated. Version 1 and selected modes keep
their existing descriptor contracts. Real J1/E assertions remain pending.

## F3 harness protocol

The runner copies and hashes the supplied executable. It invokes:

```text
<private-harness> --backend-run <private-run.json> --backend-result <private-result.json>
```

`backend-run.json` has `version=1`, `contractIdentity`, `project`, `composeFile`,
`gatewayURL`, `origin`, `sinkURLs`, `secrets`, `requirements`, `evidenceDirectory`,
`sources`, and `images`. These are the actual project, generated Compose file,
loopback endpoints, synthetic credentials, inspected image IDs and source
context manifests for this run. The descriptor and environment files are
private, temporary and removed after the run. Never copy them into public
observations. Use the same exact Compose file and project for restart commands.
Keep Auth/Feedback volumes during scenario resets. F3 must validate ownership
before manipulating containers and preserve authoritative clocks and outcomes.

F3 records bounded, redacted request/response and scenario observations under
`evidenceDirectory`. It writes a result with exactly `status`,
`contractIdentity`, and `passed`. Success requires `status="passed"`, the same
contract identity, and every requested assertion ID exactly once. Extra,
duplicate or missing IDs fail. A nonzero harness exit is preserved. A zero exit
without the complete result also fails. A caller must supply the complete
reviewed group assertion set; orchestration success alone does not prove full
backend acceptance. The F1 acceptance matrix and J2 completeness check own that
coverage boundary.

## Evidence and cleanup

A retained unique subdirectory contains `evidence.json`. Evidence is written
before input/source/build/start failures. It includes Factory HEAD/dirty digest,
the running runner binary digest, reviewed input/harness digests, requested refs,
source manifests/context hashes, attempted build refs, inspected image IDs,
container IDs/image bindings, redacted topology facts, assertion IDs, stages,
exit status and cleanup outcomes. Dirty builds use run-specific local tags and
always record `published=false`; an OCI base revision alone is not their identity.
External command output and environment values are not copied into evidence.

Cleanup uses a separate bounded context after success, early failure, partial
start or cancellation. It verifies container project/service labels before
`compose down --volumes --remove-orphans`, then verifies that project-labelled
containers, volumes and networks are absent. It inspects attempted image tags,
including a build whose reply was lost, and removes only tags with the matching
run label. It verifies tag removal. It does not prune images or remove a sink
image. Cleanup failure makes a successful run fail; an existing nonzero harness
status remains unchanged. Failed ownership checks leave unverified resources
untouched and record failed cleanup. Use the retained project identity to
investigate those resources. Temporary copied sources and credentials are removed.

## Runner checks

At the Factory root, `make check-tools` runs the runner module's format, build,
compiler/test compilation, pinned lint, architecture and complete race-test gates.
`make go-readiness` checks the three-module inventory. To validate generated
backend Compose without building images or starting containers:

```sh
GOWORK=off go test -tags composeintegration -run '^TestBackendCompose' -count=1 ./...
```

Run that command in this nested module. Orchestration tests create only temporary
Git fixtures and command doubles. Set `UMIR_BACKEND_TEST_EVIDENCE` to an owned
absolute directory to retain their redacted evidence snapshots. Complete Factory
and service-dependent gates remain F4/J1. The runner does not amend C2, change
selected image pins, launch Frontend, send real-recipient mail or publish sources.

## Operational fixture descriptor version 2

Version 2 uses the same CLI invocation and assertion/result protocol as version 1.
It adds the required `fixtures` member. Version 1 remains supported. The bootstrap
password in both versions is now 16 cryptographic random bytes encoded as exactly
32 lowercase ASCII hex characters. Other private service secrets remain 64 hex
characters. This fixes the frozen password-limit violation.

The fixture input has exactly `protocol`, `bindingsSHA256`, `fixtureID`,
`initialInstant`, `bootstrap`, `owners`, and `sinkSource`. The protocol is
`umir-operational-fixtures/1.1.0`. The reviewed bindings digest is
`af215b378140b8e690523fc537007679b8c2599ed3039f7f2a0c651a64c59410`.
The authoritative schemas and bindings are in the approved operational-fixtures
feedback-observation-correction revision, source identity
`sha256:1f585cce70d8aaf9422d769c1c804ccb3e7b6e0e318c383a8cedbc5cf91310de`.
Input parsing, provisioning and readiness refuse stale or wrong digests, including
the predecessor `de9c7bfc573ad0887be0f00b1ddc2495939469f58f3524c6dbb66a521fed385e`.
Do not use example source refs or placeholder digests as
launch inputs.

This revision keeps protocol 1.1.0 and the version-2 input, run and owner/sink
configuration shapes. F3 and the real Feedback owner implement exact
namespace/submission pair observation, fresh control request IDs for business
replay and reopen, and cached exact control replay. The runner supplies the
reviewed binding identity; it does not add Feedback operations to Gateway or
Serving. Real provider and J1/E acceptance remain pending.

Use a fresh UUID v4 and an explicit RFC3339 instant in 2000–2099, with at most
nanosecond precision. Use two-digit time fields, a period for fractional seconds,
and `Z` or a signed offset with hours 00–23 and minutes 00–59. Invalid calendar
dates, comma fractions and precision above nine digits fail before build.
The runner preserves the supplied valid instant, precision and offset.
Bootstrap must use the exact approved initial User, Auth
password-file reference, state file and initialization mode. The five owner
profiles are `<owner>-real-v2`. Each service must use
`tests/fixtures/Dockerfile`, command `["serve"]`, and healthcheck
`["CMD","/fixture-server","healthcheck"]`. Private ports are 8080 except Gateway
8082; Gateway publishes container port 8080. Auth and Feedback retain their
separate `/var/lib/umir-<owner>` volumes. Business owners have no durable mount.
The only optional environment entry is the approved `SSL_CERT_FILE` for Auth
or Feedback. Credentials and config use files.

When `sinkSource=null`, both sinks require reviewed digest-pinned images.
Otherwise, `sinkSource` has exactly `path`, `ref`, `sourceMode`, `files`,
and `dockerfile`. It must identify the complete reviewed Factory Git checkout,
known origin and exact HEAD, with Dockerfile
`tests/http/fixtures/sinks/Dockerfile` and the real nested `tests/http/go.mod`.
Both sink entries must omit `image`; mixed modes fail. Local builds use only
`--build-arg SINK=smtp` or `SINK=notification`, run-labelled local tags and
private snapshots. Their inspected image IDs and source/context identities enter
evidence. Cleanup includes attempted local sink tags, including a lost build reply.
Pinned existing sink images are preserved. SMTP ports are 2525/8025 and notification
ports are 3128/8026. The optional sink command is `["serve"]`.

The runner creates per-consumer mode-0600 files in private mode-0700 directories.
The credential-target matrix restricts every copy. It generates a run CA, verified
SMTP/Telegram server certificates, distinct control credentials, synthetic
SMTP/proxy credentials and a bot token with the approved `1:` plus 32-hex format.
Owner and sink `credentialFiles` exclude every `fixture-*` control reference.
The complete private write/mount inventory retains each control file; its reference
appears separately as `controlCredentialFile`. Serving has exactly
`serving-auth`, `auth-serving`, `gateway-serving`, and `serving-warehouse`
in its service map. Its separate control reference is `fixture-serving`.

`serving-auth` is independently generated from 32 cryptographic random bytes
encoded as 64 lowercase hex characters. Its copies reach only Serving, Auth and
the private harness. Auth must use its copy for the real Serving registry/JWKS
caller; `auth-serving` remains the distinct inbound lifecycle credential.
Missing, malformed or reused service/control material fails before launch.
The harness inventory is derived from the reviewed service pairs, bootstrap login
and seven target-specific controls; delivery credentials and signing keys are
excluded. The target regression fixture retains the exact reviewed matrix and
bindings digest. It does not receive Auth signing keys. Only required login/service/control copies
reach the harness. Generated credential values stay out of Compose, version-2
descriptors, diagnostics and retained evidence.

Each container runs as UID/GID 65532 with a read-only root filesystem. Separate
read-only parent-directory mounts provide config and credentials. A writable
mode-0700 tmpfs at `/run/umir-fixtures` provides the control socket and private
output storage; nested config/credential mounts remain read-only. Host files keep
their private owner and permissions. Docker Desktop UID translation is not assumed:
the runner verifies the actual container-visible file inventory, bytes, mode 0600
and UID/GID 65532 through bounded private copies. A mismatch fails before the
harness. Archive inventory checks keep paths relative to the requested `secrets`
or `config` root. They allow direct files and the matching Docker `cp` root prefix;
undeclared nested files, directories and foreign root prefixes fail. Binary copies
must contain only the requested direct binary path. J1 must qualify this mount
mechanism on the execution host. Do not widen
permissions to make an unreadable mount pass.

Before private copy or control execution, the runner verifies all recorded
project/service labels and image IDs. It hashes the actual server/helper binaries
from the owned containers. It sends bounded authenticated `fixture.ready` requests
through the approved `/fixture-control request` helper over stdin. The response
must identify the owner, project, protocol, process UUID and exact server/config
digests. The config digest is SHA-256 of the compact generated JSON file bytes.
All owners must advertise readiness and clock read/set/checkpoint capabilities;
Auth must also advertise `owner.jobs.discover`. Missing, malformed, oversized or
mismatched provider evidence fails closed. No control TCP listener is added.

`backend-run.json` version 2 omits `secrets`. It contains `credentialFiles`
with logical harness references and `fixtures` with the approved protocol/bindings,
fixture clock metadata, bootstrap binding digest and actual owner binary/config
identities/capabilities. `fixtures.harnessPrivateRoot` is private temporary storage.
Resolve a harness reference's `/run/umir-fixtures/secrets/` suffix beneath that root;
never read host `/run`. Owner config directories are siblings at
`<parent-of-harnessPrivateRoot>/<owner>/config`.

For a restart, F3 must first verify the recorded container, image, helper and
instance identities and obtain `clock.checkpoint`. Validate the returned fixture,
owner, instance, generation, healthy consumers and checkpoint digest. Stop only
that verified owner. Read its existing private config, replace only
`initialInstant` and `clockGeneration` with the observed checkpoint, write a new
mode-0600 regular file in the same private config directory, and atomically rename
it over `config.json`. Preserve ownership, all other fields and Auth/Feedback
volumes. The parent mount permits replacement to reach the container. Restart
through the exact recorded project/Compose file. Require a new process instance,
equal fixture/instant/generation, healthy clock consumers and fresh binary/config
readiness identities before continuing. A crash without a proven checkpoint makes
time-dependent assertions incomplete. F3 owns this scenario procedure; there is
no new runner restart flag.

F2 implements runner support and verifies it with orchestration doubles plus the
real Compose configuration parser. F3 owns actual sink/harness source. Owner
wrappers, legitimate Auth Git identity, host mount qualification, real capability
readback, bootstrap/login and J1/E acceptance remain pending. Specification approval,
image build or fixture readiness alone does not prove a business assertion.
