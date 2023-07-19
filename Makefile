.PHONY: bin/depot
bin/depot:
	go build -o $@ ./cmd/depot

.PHONY: bin/depot-docker-shim
bin/depot-docker-shim:
	go build -o $@ ./cmd/depot-docker-shim

.PHONY: clean
clean:
	rm -rf ./bin

.PHONY: generate
generate:
	buf generate
