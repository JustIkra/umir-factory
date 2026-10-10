.PHONY: tools go-readiness
tools:
	bash scripts/install-go-quality.sh
go-readiness:
	bash scripts/check-go-readiness.sh
