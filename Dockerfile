FROM --platform=$BUILDPLATFORM golang:1.24.4 AS build
WORKDIR /src
ARG LDFLAGS
ARG TARGETARCH
RUN mkdir /out
RUN \
  --mount=target=. \
  --mount=type=cache,target=/go/pkg/mod \
  GOARCH=${TARGETARCH} CGO_ENABLED=0 \
  go build -ldflags="${LDFLAGS}" \
  -o /out/ ./cmd/...

FROM ubuntu:24.04

RUN apt-get update && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*
COPY entrypoint.sh /usr/bin/entrypoint.sh
COPY --from=build /out/depot /usr/bin/depot
COPY --from=build /out/buildkitd /usr/bin/buildkitd
COPY --from=build /out/buildctl /usr/bin/buildctl

ENTRYPOINT ["/usr/bin/entrypoint.sh"]
