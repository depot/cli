FROM busybox:1.36 as build
RUN echo builtin > /message.txt

FROM scratch AS artifact
COPY --from=build /message.txt /
