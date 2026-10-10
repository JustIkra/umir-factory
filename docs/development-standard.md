# Go development standard

Apply this standard to Go work on this device. Repositories commit effective configuration, tool versions and check commands. Explicit project contracts take precedence. Read the project memory map before broad exploration.

The Git and release conventions also apply to other languages unless a project defines another policy. Go tooling and module-path rules apply only to Go modules.

## Architecture and types

- Direct source dependencies toward business rules: adapters depend on application and domain; application does not depend on adapters.
- Give each business state and decision one owner.
- Define small interfaces at their consumer. Ordinary constructors return concrete implementations.
- Keep IO, HTTP/ORM models and environment access in adapters or process composition.
- Inject time, identifiers and external mechanisms. Domain calculations do not read clocks or schedule work. This includes time.Since and time.Until; calculate differences from supplied timestamps instead.
- Use defined types for identifiers and quantities where mixing values is an error. Validate business invariants explicitly.
- Keep public contracts typed and independent of mutable storage state.
- Protect slices, maps and referenced objects with encapsulation and copies where isolation matters.
- Keep any, reflection and dynamic decoding inside the concrete boundary that needs them.
- Handle errors and cleanup failures. Preserve causes and use errors.Is/errors.As for wrapped errors.
- Give goroutines an owner, cancellation and a completion path.
- Use Europe/Moscow for domain and user-facing time unless the project specifies another zone.
- Keep fixtures, demo data and fault injection in tests/examples. Remove replaced code.

## Checks

The baseline is compiler/test compilation, golangci-lint v2, gofmt/goimports, architecture checks and focused behavioral tests. Device tools: golangci-lint 2.14.0 and go-arch-lint 1.19.0. Repository pins are authoritative.

Format changed files. Keep unrelated mass formatting separate. Do not copy Python line-length limits into Go.

Run the repository's full acceptance command before handoff. Concurrent code needs race tests on a supported platform. Race detection checks executed paths; a linter does not prove business behavior or copy isolation.

Fix causes before suppressions. A necessary nolint names one check and states a concrete reason. Do not hide failing tests, architectural violations or business errors with broad exclusions, skipped tests or changed-lines-only final checks. Generated-file exclusions require real generated-file markers and explicit scope.

Commit required go.mod/go.sum changes. Keep quality tools outside production dependencies. Pin versions and pass repository config paths explicitly; implicit home-directory configs must not control CI.

Service checks use GOWORK=off. A composed harness uses an explicit workspace and declared revisions. Inventory nested modules; root ./... is insufficient. Missing expected modules fail readiness. The shared template runner supports one root module (`.`) and rejects declared nested modules explicitly. A multi-module project must provide effective gates for every module. Library-only modules compile without executable output; binaries stay in owned temporary storage.

## Git work

Use one coherent change per branch. Prefer feat/<topic>, fix/<topic>, chore/<topic> or docs/<topic> unless the project defines another convention. Substantial independent work can use isolated worktrees.

Inspect status, unstaged diff and staged diff. Preserve unrelated local edits. Stage explicit paths or reviewed hunks. Keep commits independently understandable. Do not change the active checkout or discard work as a side effect.

Use Conventional Commits: type(scope): imperative summary. Types include feat, fix, refactor, test, docs, build, ci and chore. Mark incompatible changes with ! or BREAKING CHANGE and describe migration. Keep subjects concise. Do not add Co-Authored-By or generated-by footers.

Run required checks and git diff --check. Read git diff --cached before committing. Report actual checked commands and unavailable environment gates.

Rebase private/unpublished work for cleanup. Do not rewrite shared history or move published tags. A force push needs specific authorization; force-with-lease is a safeguard, not permission or a universal concurrency guarantee.

Do not use reset --hard, clean -fd, stash changes or automatic conflict resolutions to discard another person's work. Resolve against the actual contract and rerun affected checks.

Track required lockfiles. Exclude secrets, local workspaces, personal replace paths, tool caches and build output. Do not change global Git configuration or bypass hooks merely to obtain a passing check.

## Versions and releases

Declare public compatibility surfaces in README: exported Go contracts, HTTP inputs/results/error states and operational behavior clients depend on. Internal refactoring is not automatically a breaking API change.

Use Semantic Versioning. For stable APIs, incompatible changes increment major, compatible additions increment minor and compatible fixes increment patch. Select the highest impact in the release; reset lower components when increasing major/minor.

Version 0.y.z means initial development. Our pre-1.0 convention uses patch for compatible fixes and minor for additions or incompatible changes; breaking changes are still explicitly described. Publish 1.0.0 when the public contract and compatibility commitment are ready. This pre-1.0 convention is our policy, not an extra SemVer requirement.

Use prereleases such as v1.2.0-rc.1 for candidates. Build metadata does not establish different compatibility. Released versions are immutable: corrections require a new version.

Use annotated Git tags vX.Y.Z for root modules/releases. Published nested Go modules use the module directory prefix, for example tools/assembly-checks/v0.1.0. Go modules at v2+ require matching /v2 or later major module/import paths, subject to documented Go exceptions. Do not add /v1.

For Go tags, the prefix is the logical module subdirectory. Exclude a trailing major-version directory: a module stored under tools/assembly-checks/v2 uses tools/assembly-checks/v2.0.0, not tools/assembly-checks/v2/v2.0.0. The [Go module reference](https://go.dev/ref/mod) defines this distinction.

Version independently released repositories/modules independently. Do not synchronize unrelated version bumps. Record exact component versions/revisions in a system assembly.

Umir container identities stay sha-<full-commit-SHA> with OCI source/revision metadata. A SemVer release names a tested commit; it does not replace SHA pins with mutable latest tags. Factory's selected component SHAs define the assembly. API versions and service release versions are separate.

Before release, check the exact commit, reviewed release state, changelog, public compatibility and required tests. Check dependency consistency with go mod tidy and integrity with go mod verify where applicable. Assess real callers: adding an exported struct field or interface method can break them.

Record behavior, fixes, migration and relevant validation in CHANGELOG.md under Unreleased until an actual release. Creating tags, pushing releases or publishing requires the user's release instruction. Writing a policy does not authorize a release.

## Sources

Context7 supplied documentation from /golangci/golangci-lint, /websites/go_dev_doc, /fe3dback/go-arch-lint, /websites/git-scm_doc and /semantic-release/semantic-release. Semantic-release supplied documentation only and is not a required installed tool.

- [Go interfaces](https://go.dev/wiki/CodeReviewComments#interfaces)
- [Go module layout](https://go.dev/doc/modules/layout)
- [Go release workflow](https://go.dev/doc/modules/release-workflow)
- [Go module versions](https://go.dev/doc/modules/version-numbers)
- [Go module reference](https://go.dev/ref/mod)
- [golangci-lint configuration](https://golangci-lint.run/docs/configuration/file/)
- [go-arch-lint](https://github.com/fe3dback/go-arch-lint)
- [Git worktrees](https://git-scm.com/docs/git-worktree)
- [Git tags](https://git-scm.com/docs/git-tag)
- [Git push safeguards](https://git-scm.com/docs/git-push)
- [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/)
- [Semantic Versioning](https://semver.org/spec/v2.0.0.html)

The selected checks, annotated-tag preference and pre-1.0 convention are working-policy choices. Update effective project configs when packages, dependencies or public contracts change.
