.PHONY: bin/depot
bin/depot:
	go build -o $@ ./cmd/depot

.PHONY: clean
clean:
	rm -rf ./bin

.PHONY: generate
generate:
	buf generate
