FROM busybox:1.36 AS base
ARG MESSAGE=base
RUN mkdir /out && echo "$MESSAGE" > /out/base.txt

FROM base AS child
RUN echo child >> /out/base.txt

FROM scratch AS artifact
COPY --from=child /out /
