GO ?= go
COMPOSE ?= docker compose -f deploy/docker/docker-compose.yml

.PHONY: build test race bench lint fmt up down logs stats topology clean

build:
	$(GO) build -trimpath ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

bench:
	$(GO) test -run=XXX -bench=. -benchmem ./internal/...

fmt:
	$(GO) fmt ./...
	$(GO) vet ./...

## up: tüm yığını ayağa kaldırır (clickhouse + nabiz + .NET örnekleri + yük)
up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f collector

## stats: collector'ın iç sayaçları
stats:
	@curl -s localhost:8888/stats | python3 -m json.tool

## topology: isteklerden çıkarılan servis grafiği
topology:
	@curl -s "localhost:8080/api/v1/topology?from=1h" | python3 -m json.tool

## services: RED metrikleri
services:
	@curl -s "localhost:8080/api/v1/services?from=1h" | python3 -m json.tool

clean:
	rm -rf bin/
