# syntax=docker/dockerfile:1-labs
FROM busybox:1.36 AS build
RUN --security=insecure mkdir /out && (cat /proc/self/status | grep CapEff > /out/caps.txt)
RUN --network=host echo network > /out/network.txt

FROM scratch
COPY --from=build /out /
