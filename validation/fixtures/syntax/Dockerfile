# syntax=docker/dockerfile:1.7
FROM busybox:1.36 AS build
RUN --mount=type=cache,target=/cache mkdir -p /out && echo cached > /cache/value && cp /cache/value /out/value.txt
COPY <<EOT /out/heredoc.txt
written by a heredoc
EOT

FROM scratch
COPY --from=build /out /
