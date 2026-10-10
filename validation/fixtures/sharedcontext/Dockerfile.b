FROM busybox:1.36 AS build
COPY . /src
RUN mkdir /out && ls /src | sort > /out/files-b.txt

FROM scratch
COPY --from=build /out /
