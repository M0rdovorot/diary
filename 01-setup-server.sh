#!/usr/bin/env bash
# Первичная настройка сервера Timeweb Cloud MSK 50 (Ubuntu 26.04)
# Запускать под root: ssh root@ВАШ_IP, затем bash 01-setup-server.sh
#
# ВАЖНО: перед запуском замените переменные ниже под себя.
set -euo pipefail

NEW_USER="deploy"          # имя нового пользователя (не root)
SSH_PUBLIC_KEY=""          # сюда вставьте ваш публичный SSH-ключ (cat ~/.ssh/id_ed25519.pub на своём компьютере)

if [[ -z "$SSH_PUBLIC_KEY" ]]; then
  echo "ОШИБКА: заполните переменную SSH_PUBLIC_KEY перед запуском." >&2
  exit 1
fi

echo "==> Обновление системы"
apt update && apt -y upgrade

echo "==> Создание пользователя $NEW_USER"
if ! id -u "$NEW_USER" >/dev/null 2>&1; then
  adduser --disabled-password --gecos "" "$NEW_USER"
  usermod -aG sudo "$NEW_USER"
fi

echo "==> Настройка SSH-ключа для $NEW_USER"
mkdir -p /home/"$NEW_USER"/.ssh
echo "$SSH_PUBLIC_KEY" > /home/"$NEW_USER"/.ssh/authorized_keys
chmod 700 /home/"$NEW_USER"/.ssh
chmod 600 /home/"$NEW_USER"/.ssh/authorized_keys
chown -R "$NEW_USER":"$NEW_USER" /home/"$NEW_USER"/.ssh

echo "==> Отключение входа по паролю и root по SSH"
sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config
sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config
systemctl restart ssh

echo "==> Настройка firewall (UFW)"
apt -y install ufw
ufw allow OpenSSH
ufw --force enable

echo "==> Установка fail2ban"
apt -y install fail2ban
systemctl enable --now fail2ban

echo "==> Установка Docker Engine (официальный репозиторий)"
apt -y install ca-certificates curl gnupg
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \
  $(. /etc/os-release && echo "${VERSION_CODENAME}") stable" \
  | tee /etc/apt/sources.list.d/docker.list > /dev/null
apt update
apt -y install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

echo "==> Добавление $NEW_USER в группу docker"
usermod -aG docker "$NEW_USER"

echo "==> Своп-файл 4 ГБ (запас для LLM/Whisper при пиковой нагрузке)"
if [[ ! -f /swapfile ]]; then
  fallocate -l 4G /swapfile
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

echo "==> Готово. Дальше заходите под новым пользователем:"
echo "    ssh $NEW_USER@ВАШ_IP"
echo "    и проверяйте: docker run hello-world"
