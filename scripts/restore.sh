#!/usr/bin/env bash
# Восстановление из шифрованного бэкапа в НОВУЮ базу (существующую не трогает).
# Использование: scripts/restore.sh backups/diary-....sql.gz.enc [имя_новой_базы]
# По умолчанию имя базы: diary_restored. Чтобы заменить рабочую базу, сначала проверьте
# восстановленную, потом переименуйте/удалите старую вручную.
set -euo pipefail
cd "$(dirname "$0")/.."

file="${1:?укажите файл бэкапа}"
target="${2:-diary_restored}"
# значения из окружения важнее .env (удобно для cron и проверок)
_pp="${BACKUP_PASSPHRASE:-}"
[ -f .env ] && { set -a; . ./.env; set +a; }
[ -n "$_pp" ] && BACKUP_PASSPHRASE="$_pp"
: "${BACKUP_PASSPHRASE:?задайте BACKUP_PASSPHRASE в .env}"

if docker compose exec -T db psql -U diary -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='$target'" | grep -q 1; then
  echo "база $target уже существует — укажите другое имя или удалите её" >&2
  exit 1
fi

docker compose exec -T db createdb -U diary "$target"
# при ошибке не оставляем недовосстановленную базу
trap 'docker compose exec -T db dropdb -U diary --if-exists "$target" >/dev/null 2>&1; echo "восстановление не удалось, база $target удалена" >&2' ERR
openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 -pass env:BACKUP_PASSPHRASE -in "$file" \
  | gunzip \
  | docker compose exec -T db psql -U diary -d "$target" -v ON_ERROR_STOP=1 -q -o /dev/null
trap - ERR
echo "OK: восстановлено в базу $target"
