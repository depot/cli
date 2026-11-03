FROM busybox:1.36 AS build
ARG SOURCE_DATE_EPOCH
RUN echo "sde=$SOURCE_DATE_EPOCH" > /s

FROM scratch
COPY --from=build /s /s
