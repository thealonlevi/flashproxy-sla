.PHONY: build origin worker website verify keygen vet test up demo clean

build: origin worker website verify keygen

origin:
	CGO_ENABLED=0 go build -trimpath -o bin/origin ./cmd/origin

worker:
	CGO_ENABLED=0 go build -trimpath -o bin/worker ./cmd/worker

website:
	CGO_ENABLED=0 go build -trimpath -o bin/website ./cmd/website

verify:
	CGO_ENABLED=0 go build -trimpath -o bin/verify ./cmd/verify

keygen:
	CGO_ENABLED=0 go build -trimpath -o bin/keygen ./cmd/keygen

vet:
	go vet ./...

test:
	go test ./...

up:
	docker compose up --build

demo:
	docker compose --profile demo up --build

clean:
	rm -rf bin out
