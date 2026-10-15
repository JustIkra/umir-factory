.PHONY: tools go-readiness fmt fmt-all fmt-check build lint arch test check check-tools check-business check-http
export FILES QUALITY_MODULE

tools:
	bash scripts/install-go-quality.sh
go-readiness:
	bash scripts/check-go-readiness.sh
fmt:
	bash scripts/go-quality.sh fmt
fmt-all:
	bash scripts/go-quality.sh fmt-all
fmt-check build lint arch test check check-tools check-business check-http:
	bash scripts/go-quality.sh $@

.PHONY: check-contract
check-contract:
	node --test tests/infra/completion/prepare.test.mjs tests/infra/completion/compose.test.mjs tests/infra/docs.test.mjs
	cd tests/http && GOWORK=off go test ./cmd/acceptance -run 'Test(Registration|Completion|UIAmendment)' -count=1
