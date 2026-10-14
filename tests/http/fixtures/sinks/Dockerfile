FROM golang:1.26.9-alpine AS build
ARG SINK
WORKDIR /src/tests/http
COPY tests/http/go.mod ./
RUN GOWORK=off go mod download
COPY tests/http/ ./
RUN case "$SINK" in smtp|notification) ;; *) exit 1 ;; esac && CGO_ENABLED=0 GOWORK=off go build -trimpath -ldflags="-X main.sinkRole=$SINK" -o /out/fixture-sink ./cmd/fault-proxy

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && mkdir -p /run/umir-fixtures/private && chown -R 65532:65532 /run/umir-fixtures && chmod 0700 /run/umir-fixtures/private
COPY --from=build /out/fixture-sink /fixture-sink
USER 65532:65532
ENTRYPOINT ["/fixture-sink"]
CMD ["serve"]
HEALTHCHECK --interval=2s --timeout=2s --retries=10 CMD ["/fixture-sink", "healthcheck"]
