.PHONY: run build vet test db-up db-down psql backup restore docker-up docker-down docker-logs web-up web-down web-logs tidy clean-data

BIN := bin/bot

# Загрузить .env в окружение рецепта (включая NO_PROXY/HTTPS_PROXY, которые читает Go).
ENV := set -a && . ./.env && set +a

# Поднять Postgres в Docker и дождаться готовности.
db-up:
	@test -f .env || { echo ".env не найден: cp .env.example .env"; exit 1; }
	docker compose up -d --wait db

db-down:
	docker compose stop db

psql:
	docker compose exec db psql -U diary -d diary

# Шифрованный бэкап базы в ./backups (пароль — BACKUP_PASSPHRASE из .env).
backup: db-up
	scripts/backup.sh

# Восстановление в НОВУЮ базу: make restore FILE=backups/diary-....sql.gz.enc [DB=diary_restored]
restore: db-up
	scripts/restore.sh $(FILE) $(DB)

# Запуск локально (база — в Docker на localhost:5432).
run: db-up
	$(ENV) && DATA_DIR=$${DATA_DIR:-./data} go run ./cmd/bot

build:
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/bot

vet:
	go vet ./...

# Тесты, включая интеграционные: нужна отдельная БД diary_test (создаётся здесь).
# ВНИМАНИЕ: тесты store пересоздают схему public в diary_test — рабочую базу не трогают.
test: db-up
	docker compose exec -T db psql -U diary -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='diary_test'" | grep -q 1 \
		|| docker compose exec -T db createdb -U diary diary_test
	$(ENV) && TEST_DATABASE_URL="postgres://diary:$$POSTGRES_PASSWORD@localhost:5432/diary_test?sslmode=disable" go test -p 1 ./...

tidy:
	go mod tidy

# Всё в Docker (бот + база), как на сервере.
docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f bot

# HTTPS-прокси (Caddy) для Mini App; домен — WEBAPP_DOMAIN из .env.
web-up:
	docker compose --profile web up -d caddy

web-down:
	docker compose --profile web stop caddy

web-logs:
	docker compose --profile web logs -f caddy

clean-data:
	rm -rf data
