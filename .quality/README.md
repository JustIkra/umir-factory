# Factory Go adoption

These module-specific configurations were prepared for tools/assembly-checks and tests/business. Both modules now exist. The effective `make check` runner uses the repository `.golangci.yml` together with `.go-arch-lint.yml` and `.go-arch-business.yml`; these files remain reviewed policy inputs and are not invoked by that runner.

Run `make go-readiness` from the factory. Missing or unclassified modules fail. This inventory gate does not replace build, lint, architecture or race-test checks.

The repository-level effective configs run with the pinned tool versions and package maps. Run independent tooling checks with GOWORK=off. Run business lint/tests through an explicit controlled workspace from the established runner. Keep provider imports restricted to public contract/bootstrap packages.

CI preserves image/Compose/smoke checks and runs the runner gates. Keep these module-specific policy files aligned if they are later adopted. Passing readiness alone is not a runtime business verification.
