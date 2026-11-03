FROM busybox:1.36 AS build
RUN --mount=type=secret,id=token --mount=type=secret,id=envsecret \
    mkdir /out && cat /run/secrets/token > /out/token.txt && cat /run/secrets/envsecret > /out/env.txt

FROM scratch
COPY --from=build /out /
