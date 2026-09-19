.PHONY: build up down logs test test-integration test-chaos lint web-install web-dev web-build

build:
	go build ./...

up:
	docker-compose --env-file .env up --build -d

down:
	docker-compose down -v

logs:
	docker-compose logs -f

lint:
	go vet ./...
	gofmt -l .

test:
	go test -race ./...

test-integration:
	docker-compose --env-file .env up -d postgres nats
	go test -tags=integration -race ./...

test-chaos:
	docker-compose --env-file .env up -d postgres nats
	go test -tags=chaos -race ./test/chaos/...

web-install:
	cd web && npm install

web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build
