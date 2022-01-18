FROM golang:1.17-alpine AS build
WORKDIR /src
RUN \
  --mount=target=. \
  --mount=target=/go/pkg/mod,type=cache \
  go build -o /out/usr/bin/depot
RUN ln -s /usr/bin/depot /out/usr/bin/buildctl

FROM alpine:3 AS mono
COPY --from=build /out /
ENTRYPOINT [ "/usr/bin/depot" ]
