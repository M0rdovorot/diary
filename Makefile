.PHONY: run build vet test docker-up docker-down docker-logs tidy clean-data

BIN := bin/bot

# Запуск локально: переменные из .env экспортируются в окружение процесса
# (включая NO_PROXY/HTTPS_PROXY, которые читает Go).
run:
	@test -f .env || { echo ".env не найден: cp .env.example .env"; exit 1; }
	set -a && . ./.env && set +a && DATA_DIR=$${DATA_DIR:-./data} go run ./cmd/bot

build:
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/bot

vet:
	go vet ./...

test:
	go test ./...

tidy:
	go mod tidy

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f bot

clean-data:
	rm -rf data
