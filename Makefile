.PHONY: tools go-readiness fmt fmt-all fmt-check build lint arch test check check-tools check-business check-http check-durable integration
export FILES QUALITY_MODULE

tools:
	bash scripts/install-go-quality.sh
go-readiness:
	bash scripts/check-go-readiness.sh
fmt:
	bash scripts/go-quality.sh fmt
fmt-all:
	bash scripts/go-quality.sh fmt-all
fmt-check build lint arch test check check-tools check-business check-http check-durable:
	bash scripts/go-quality.sh $@

.PHONY: check-contract check-infra
check-contract:
	node --test tests/infra/completion/prepare.test.mjs tests/infra/completion/compose.test.mjs tests/infra/completion/expiry.test.mjs tests/infra/completion/private-init.test.mjs tests/infra/completion/transport-health.test.mjs tests/infra/docs.test.mjs
	cd tests/http && GOWORK=off go test ./cmd/acceptance -run 'Test(Registration|Completion|UIAmendment|Durable)' -count=1

check-infra:
	bash tests/infra/durable-test-infra.test.sh
	bash tests/infra/durable-copy.test.sh
	node --test tests/infra/durable-*.test.mjs

integration:
	cd reference/durable && GOWORK=off go test -tags integration -p 1 -race -count=1 ./...
