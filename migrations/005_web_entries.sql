-- Записи, добавленные через Mini App, не связаны с сообщением Telegram: telegram_message_id = NULL.
-- Уникальность (chat, message) для сообщений сохраняется, NULL-значения в ней не конфликтуют.
ALTER TABLE voice_messages ALTER COLUMN telegram_message_id DROP NOT NULL;
