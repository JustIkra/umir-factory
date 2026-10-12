# Repository instructions

## Shared development rules

- Read the project memory map before broad exploration. Keep developer commands and architecture in README.md.
- Direct source dependencies toward business rules. Define small ports at their consumer and assemble implementations explicitly.
- Inject time, IDs and external mechanisms. Use Europe/Moscow unless the contract specifies another zone.
- Keep contracts typed, validate business values, handle errors and preserve wrapped causes.
- Protect shared mutable data and give goroutines an owner, cancellation and completion.
- Keep fixtures and fault injection in tests/examples. Remove replaced code. Comment only non-obvious behavior.
- Preserve unrelated edits. Inspect status and both diffs; stage reviewed paths. Do not hide failures with exclusions or skipped tests.
- Use Conventional Commits. Mark incompatible behavior with ! or BREAKING CHANGE and document migration. No Co-Authored-By or generated-by footers.
- Use SemVer for public contracts. Before 1.0, compatible fixes increment patch; additions/breaking changes increment minor. Published versions/tags are immutable.
- Use annotated vX.Y.Z release tags. Nested published Go modules prefix the module directory; Go v2+ uses matching major module/import paths.
- Release repositories/modules independently. Umir images remain sha-<full-SHA>; factory pins exact component identities.
- Do not rewrite shared history or create/push releases without specific authorization.
- Record actual checks and limitations. Run git diff --check before handoff.

## Repository boundary

Factory owns reproducible assembly and integration checks. The bridge imports public contract/bootstrap and translates values without business decisions. The Go assembly runner replaces the former Python image and smoke scripts; keep those scripts removed. Preserve image publication/source/OCI/HEALTHCHECK, Compose and smoke checks.

## Documentation

Follow the document ownership map in README.md. Update developer commands, actual stack and operational behavior in the same PR as implementation. Keep recommended, approved/planned and implemented technologies distinct. Business rules belong to docs/business-core.md; public/private wire contracts belong to docs/api. Keep AGENTS.md concise, link canonical documents, and record relevant changes under Unreleased.

## Verification and readiness

Run `make go-readiness` to verify all three declared nested modules: `tools/assembly-checks`, `tests/business` and `tests/http`. All modules have active build, lint, architecture and race-test gates in `make check`. Tooling and HTTP module checks use GOWORK=off; business lint/tests use a controlled workspace from the established runner. Assembly CI preserves Compose, image and smoke checks. The business suite runs automatically against the exact selected service commits after assembly verification. HTTP candidate acceptance builds four exact source revisions; selected acceptance uses published images and requires the selected HTTP marker. Auth candidate acceptance (`--candidate --auth`) requires the `x-umir-auth-acceptance: candidate` marker in `compose.auth.yaml` and the one published frontend port; never detect auth from responses. Both modes require service evidence and owned-project cleanup. The shared verifier uses a temporary Compose project and ephemeral loopback ports; preserve its failure reporting.

Do not create dummy modules to pass readiness.
