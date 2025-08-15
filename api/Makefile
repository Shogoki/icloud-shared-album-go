start:
	go run main.go

dev:
	go run main.go

build:
	go build -o icloud-api-go main.go

clean:
	rm -f icloud-api-go

docker-build:
	docker build -t shogoki/icloud-api-go -f Dockerfile ..

docker-run:
	docker run -p 8000:80 -e PORT=80 shogoki/icloud-api-go

.PHONY: start dev build clean docker-build docker-run
