-- Начальная схема. Подробности и правила слияния — в CLAUDE.md, раздел «Модель данных».

-- 1. Сырой слой: голосовые и их расшифровки. Аудио не храним, текст — навсегда.
CREATE TABLE voice_messages (
    id                   BIGSERIAL PRIMARY KEY,
    telegram_chat_id     BIGINT      NOT NULL,
    telegram_message_id  BIGINT      NOT NULL,
    sent_at              TIMESTAMPTZ NOT NULL,
    -- «день дневника» для момента отправки: граница дня 05:00 по Москве
    -- (голосовое в 01:00 относится к предыдущему дню)
    logical_date         DATE        NOT NULL,
    duration_sec         INT         NOT NULL,
    transcript           TEXT        NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (telegram_chat_id, telegram_message_id)
);

-- 2. Результат извлечения моделью (JSON как есть). Можно переобработать с новым промптом.
CREATE TABLE extractions (
    id             BIGSERIAL PRIMARY KEY,
    voice_id       BIGINT      NOT NULL REFERENCES voice_messages (id) ON DELETE CASCADE,
    model          TEXT        NOT NULL,
    prompt_version TEXT        NOT NULL,
    raw_json       JSONB       NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 3. Сегменты: один голосовой может рассказывать о нескольких днях.
CREATE TABLE segments (
    id            BIGSERIAL PRIMARY KEY,
    extraction_id BIGINT NOT NULL REFERENCES extractions (id) ON DELETE CASCADE,
    voice_id      BIGINT NOT NULL REFERENCES voice_messages (id) ON DELETE CASCADE,
    entry_date    DATE,                      -- день, о котором рассказано (NULL, пока не уточнён)
    date_source   TEXT   NOT NULL CHECK (date_source IN ('explicit', 'relative', 'unknown', 'user')),
    status        TEXT   NOT NULL CHECK (status IN ('pending_date', 'resolved')),
    payload       JSONB  NOT NULL,           -- поля сегмента из ответа модели
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status = 'pending_date' OR entry_date IS NOT NULL)
);
CREATE INDEX segments_pending_idx ON segments (status) WHERE status = 'pending_date';

-- 4. Наблюдения: по одной строке на поле, которое было упомянуто.
-- Не упомянуто = строки нет (NULL-значений не храним). false/0 — значит «прямо сказано: нет».
CREATE TABLE observations (
    id         BIGSERIAL PRIMARY KEY,
    segment_id BIGINT NOT NULL REFERENCES segments (id) ON DELETE CASCADE,
    voice_id   BIGINT NOT NULL REFERENCES voice_messages (id) ON DELETE CASCADE,
    entry_date DATE   NOT NULL,
    field      TEXT   NOT NULL,              -- mood, sleep_hours, water_ml, alcohol, done, ...
    value      JSONB  NOT NULL,              -- строка | число | bool | массив строк
    -- same_day: голосовое отправлено в тот же «день дневника»; retro: рассказ о другом дне
    relation   TEXT   NOT NULL CHECK (relation IN ('same_day', 'retro')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX observations_day_field_idx ON observations (entry_date, field, created_at);

-- «Карточка дня» не хранится: собирается кодом из observations по правилам слияния.
-- Позже для статистики добавим VIEW daily_metrics (одна строка на день).
