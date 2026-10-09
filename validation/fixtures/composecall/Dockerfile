FROM busybox:1.36 AS build
ARG MESSAGE=default
ARG TARGETPLATFORM
RUN mkdir /out && echo "$MESSAGE" > /out/message.txt && echo "$TARGETPLATFORM" > /out/platform.txt
COPY file.txt /out/file.txt

FROM scratch AS artifact
COPY --from=build /out /

FROM busybox:1.36 AS image
COPY --from=build /out /app
LABEL org.depot.validation=basic
ENV VALIDATION=1
CMD ["cat", "/app/message.txt"]
