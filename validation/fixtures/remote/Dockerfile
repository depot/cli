FROM busybox:1.36 AS build
ARG MESSAGE=remote
RUN mkdir /out && echo "$MESSAGE" > /out/remote.txt

FROM scratch AS artifact
COPY --from=build /out /
