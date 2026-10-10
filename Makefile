.PHONY: tools go-readiness fmt fmt-all fmt-check build lint arch test check check-tools check-business
export FILES QUALITY_MODULE

tools:
	bash scripts/install-go-quality.sh
go-readiness:
	bash scripts/check-go-readiness.sh
fmt:
	bash scripts/go-quality.sh fmt
fmt-all:
	bash scripts/go-quality.sh fmt-all
fmt-check build lint arch test check check-tools check-business:
	bash scripts/go-quality.sh $@
