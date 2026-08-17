IMAGE ?= icloud-api-go:dev

start:
	go run .

dev:
	go run .

build:
	go build -o icloud-api-go .

clean:
	rm -f icloud-api-go

vet:
	go vet ./...

test:
	go test ./...

# Build context is this directory — the API depends on the published library
# module, not on the parent tree.
docker-build:
	docker build -t $(IMAGE) .

docker-run:
	docker run --rm -p 8000:80 -e PORT=80 $(IMAGE)

.PHONY: start dev build clean vet test docker-build docker-run
