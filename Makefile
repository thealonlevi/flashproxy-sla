.PHONY: build origin collector prober vet test up demo clean

build: origin collector prober

origin:
	CGO_ENABLED=0 go build -trimpath -o bin/origin ./cmd/origin

collector:
	CGO_ENABLED=0 go build -trimpath -o bin/collector ./cmd/collector

prober:
	CGO_ENABLED=0 go build -trimpath -o bin/prober ./cmd/prober

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
