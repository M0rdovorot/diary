# Шаги 1–2: сервер + Docker + Whisper/Ollama

## 1. Настройка сервера

1. Сгенерируйте SSH-ключ на своём компьютере, если ещё нет:
   ```
   ssh-keygen -t ed25519 -C "voicebot"
   cat ~/.ssh/id_ed25519.pub
   ```
2. Скопируйте вывод и вставьте в переменную `SSH_PUBLIC_KEY` внутри `01-setup-server.sh`.
3. Загрузите скрипт на сервер и запустите под root:
   ```
   scp 01-setup-server.sh root@ВАШ_IP:~/
   ssh root@ВАШ_IP
   bash 01-setup-server.sh
   ```
4. После выполнения зайдите уже новым пользователем:
   ```
   ssh deploy@ВАШ_IP
   docker run hello-world
   ```
   Если контейнер запустился — Docker работает.

## 2. Запуск Whisper и Ollama

1. Скопируйте `docker-compose.yml` и `.env.example` на сервер в отдельную папку, например `~/voicebot/`.
2. Переименуйте `.env.example` в `.env` и заполните `TELEGRAM_BOT_TOKEN` (получить у @BotFather в Telegram) — понадобится на следующем шаге.
3. Поднимите сервисы:
   ```
   cd ~/voicebot
   docker compose up -d whisper ollama
   ```
4. Скачайте модель для Ollama (займёт несколько минут, ~2 ГБ):
   ```
   docker exec -it ollama ollama pull qwen2.5:3b-instruct-q4_K_M
   ```
5. Проверьте, что Whisper поднялся:
   ```
   docker compose logs whisper --tail=50
   ```
   Ищите строку о готовности сервиса на порту 9000.

6. Проверьте использование памяти:
   ```
   docker stats
   ```

## Что дальше

Когда оба сервиса стабильно работают — переходим к шагу 3: пишем Go-бота, который будет дёргать `whisper` и `ollama` по внутренним адресам из `.env`.
