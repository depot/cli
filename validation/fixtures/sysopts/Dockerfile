FROM busybox:1.36 AS build
RUN mkdir /out && (grep validation.invalid /etc/hosts > /out/hosts.txt || true) \
    && ulimit -n > /out/nofile.txt \
    && df -k /dev/shm | tail -1 | awk '{print $2}' > /out/shm.txt

FROM scratch
COPY --from=build /out /
