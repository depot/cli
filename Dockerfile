FROM --platform=$BUILDPLATFORM golang:1.17-alpine AS build
WORKDIR /src
ARG TARGETARCH
RUN mkdir /out
RUN \
  --mount=target=. \
  --mount=target=/go/pkg/mod,type=cache \
  GOARCH=${TARGETARCH} CGO_ENABLED=0 go build -o /out/usr/bin/depot ./cmd/depot
RUN ln -s /usr/bin/depot /out/usr/bin/buildctl

FROM alpine:3
COPY --from=build /out /
ENTRYPOINT [ "/usr/bin/depot" ]
