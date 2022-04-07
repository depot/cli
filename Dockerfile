FROM --platform=$BUILDPLATFORM golang:1.18 AS build
WORKDIR /src
ARG TARGETARCH
RUN mkdir /out
RUN \
  --mount=target=. \
  --mount=target=/go/pkg/mod,type=cache \
  GOARCH=${TARGETARCH} CGO_ENABLED=0 go build -o /out/ ./cmd/...

FROM --platform=$TARGETPLATFORM ubuntu:20.04

COPY --from=build /out/depot /usr/bin/depot
ENTRYPOINT ["/usr/bin/depot"]
