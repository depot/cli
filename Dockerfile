FROM --platform=$BUILDPLATFORM golang:1.20 AS build
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

FROM --platform=$TARGETPLATFORM ubuntu:20.04

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/depot /usr/bin/depot
# Mimics buildkitd and buildctl cli for buildx container drivers.
RUN ln -s /usr/bin/depot /usr/bin/buildctl && ln -s /usr/bin/depot /usr/bin/buildkitd
CMD ["/usr/bin/depot"]
