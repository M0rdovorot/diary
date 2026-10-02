#!/usr/bin/env bash
# Шифрованный бэкап Postgres: pg_dump -> gzip -> openssl AES-256 (пароль из BACKUP_PASSPHRASE).
# Использование: scripts/backup.sh            (база diary из docker compose, файлы в ./backups)
# Переменные: BACKUP_DIR (по умолчанию backups), BACKUP_KEEP (сколько последних хранить, 14), DB_NAME (diary).
set -euo pipefail
cd "$(dirname "$0")/.."

# значения из окружения важнее .env (удобно для cron и проверок)
_pp="${BACKUP_PASSPHRASE:-}"
[ -f .env ] && { set -a; . ./.env; set +a; }
[ -n "$_pp" ] && BACKUP_PASSPHRASE="$_pp"
: "${BACKUP_PASSPHRASE:?задайте BACKUP_PASSPHRASE в .env}"
DB_NAME="${DB_NAME:-diary}"
BACKUP_DIR="${BACKUP_DIR:-backups}"
BACKUP_KEEP="${BACKUP_KEEP:-14}"

mkdir -p "$BACKUP_DIR"
chmod 700 "$BACKUP_DIR"
out="$BACKUP_DIR/${DB_NAME}-$(date +%Y%m%d-%H%M%S).sql.gz.enc"
tmp="$out.partial"
trap 'rm -f "$tmp"' EXIT

docker compose exec -T db pg_dump -U diary --no-owner "$DB_NAME" \
  | gzip -9 \
  | openssl enc -aes-256-cbc -pbkdf2 -iter 200000 -salt -pass env:BACKUP_PASSPHRASE \
  > "$tmp"

# пустой или битый файл бэкапом не считаем
[ "$(stat -c %s "$tmp")" -gt 100 ] || { echo "бэкап подозрительно маленький, отмена" >&2; exit 1; }
mv "$tmp" "$out"
chmod 600 "$out"
trap - EXIT
echo "OK: $out ($(du -h "$out" | cut -f1))"

# ротация: оставляем BACKUP_KEEP последних
ls -1t "$BACKUP_DIR/${DB_NAME}-"*.sql.gz.enc 2>/dev/null | tail -n +"$((BACKUP_KEEP + 1))" | xargs -r rm -f --
