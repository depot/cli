OUTPUT ?= dist

.PHONY: build
build:
	CGO_ENABLED=0 gox \
		-osarch="darwin/amd64 linux/amd64 linux/arm64 linux/386" \
		-output="${OUTPUT}/depot-{{.OS}}-{{.Arch}}" \
		.

.PHONY: clean
clean:
	rm -rf ${OUTPUT}
