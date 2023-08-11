.PHONY: bin/depot
bin/depot:
	go build -o $@ ./cmd/depot

.PHONY: image
image:
	docker --context=default buildx build --builder default -t ghcr.io/depot/cli:0.0.0-dev --load .

.PHONY: clean
clean:
	rm -rf ./bin

.PHONY: generate
generate:
	buf generate
