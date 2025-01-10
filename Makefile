.PHONY: bin/depot
bin/depot:
	go build -o $@ ./cmd/depot

.PHONY: image
image:
	docker --context=default buildx build --builder default -t public.ecr.aws/depot/cli:0.0.0-dev --load .

.PHONY: npm
npm:
	cd npm && pnpm run clean && pnpm run build

.PHONY: clean
clean:
	rm -rf ./bin

.PHONY: generate
generate:
	buf generate
