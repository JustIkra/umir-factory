# Factory Go adoption

These configurations are prepared for tools/assembly-checks and tests/business. They do not create either module or replace the ongoing runner implementation.

Run make go-readiness from the factory. Missing or unclassified modules fail. Current main has no such modules, so this gate currently fails as expected; existing assembly CI remains active.

After the modules land, validate these complete configurations with the pinned tool versions and their actual package maps. Run independent tooling checks with GOWORK=off. Run business lint/tests through an explicit controlled workspace from the established runner. Keep provider imports restricted to public contract/bootstrap packages.

Connect these gates to CI together with the real modules, preserve image/Compose/smoke checks, and prove failures on forbidden imports. This preparation is not a runtime business verification or a successful Go CI adoption.
