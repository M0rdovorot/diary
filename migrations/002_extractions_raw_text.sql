-- Сырой ответ модели хранится как текст: он может быть обёрнут в ```-блок и не быть валидным JSON.
ALTER TABLE extractions RENAME COLUMN raw_json TO raw_response;
ALTER TABLE extractions ALTER COLUMN raw_response TYPE TEXT USING raw_response::text;
