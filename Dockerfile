FROM --platform=$BUILDPLATFORM golang:1.18 AS build
WORKDIR /src
ARG LDFLAGS
ARG TARGETARCH
RUN mkdir /out
RUN \
  --mount=target=. \
  --mount=target=/go/pkg/mod,type=cache \
  GOARCH=${TARGETARCH} LDFLAGS=${LDFLAGS} CGO_ENABLED=0 \
  go build \
  -ldflags="$LDFLAGS" \
  -o /out/ ./cmd/...

FROM --platform=$TARGETPLATFORM ubuntu:20.04

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/depot /usr/bin/depot
ENTRYPOINT ["/usr/bin/depot"]
