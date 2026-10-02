-- Категории записи дня редактирует пользователь (/categories). Значения по умолчанию
-- записываются из кода при старте (недостающие ключи добавляются), поэтому здесь только структура.
-- Скрытая категория (active = false) не попадает в промпт и карточки, но её данные сохраняются.
CREATE TABLE categories (
    key        TEXT PRIMARY KEY,
    title      TEXT        NOT NULL,
    kind       TEXT        NOT NULL CHECK (kind IN ('text', 'list', 'number', 'bool')),
    hint       TEXT        NOT NULL DEFAULT '',
    unit       TEXT        NOT NULL DEFAULT '',
    group_name TEXT        NOT NULL DEFAULT '',
    position   INT         NOT NULL,
    active     BOOLEAN     NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- source: voice — голосовое, text — записано текстом. confirmed: пользователь подтвердил расшифровку.
-- Уже сохранённые записи считаются подтверждёнными, чтобы не заваливать /pending.
ALTER TABLE voice_messages ADD COLUMN source TEXT NOT NULL DEFAULT 'voice' CHECK (source IN ('voice', 'text'));
ALTER TABLE voice_messages ADD COLUMN confirmed BOOLEAN NOT NULL DEFAULT true;
