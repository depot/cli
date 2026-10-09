FROM busybox:1.36 as build
MAINTAINER validation@example.com
RUN echo lint > /lint.txt

FROM scratch
COPY --from=build /lint.txt /
