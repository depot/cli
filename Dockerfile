FROM golang:1.17-alpine AS build
WORKDIR /src
RUN \
  --mount=target=. \
  --mount=target=/go/pkg/mod,type=cache \
  go build -o /usr/bin/depot

FROM alpine:3 AS mono
COPY --from=build /usr/bin/depot /usr/bin/depot
RUN ln -s /usr/bin/depot /usr/bin/buildctl
ENTRYPOINT [ "/usr/bin/depot" ]
