FROM busybox:1.36 AS build
COPY --from=extra extra.txt /out/extra.txt
COPY main.txt /out/main.txt

FROM scratch
COPY --from=build /out /
