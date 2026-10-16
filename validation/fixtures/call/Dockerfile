# syntax=docker/dockerfile:1
FROM busybox:1.36 as build
ARG MESSAGE=default
RUN echo "$MESSAGE" > /message.txt

FROM scratch AS artifact
COPY --from=build /message.txt /
