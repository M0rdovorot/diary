-- reference_date: день, к которому пользователь отнёс голосовое («рабочий день» /date).
-- Если рабочий день не выбран, совпадает с logical_date (день дневника на момент отправки).
-- От reference_date считаются «сегодня»/«вчера» и отличие основной записи от дополнения (same_day/retro).
ALTER TABLE voice_messages ADD COLUMN reference_date DATE;
UPDATE voice_messages SET reference_date = logical_date;
ALTER TABLE voice_messages ALTER COLUMN reference_date SET NOT NULL;

-- История правок расшифровок: прежняя версия не теряется.
CREATE TABLE transcript_edits (
    id        BIGSERIAL PRIMARY KEY,
    voice_id  BIGINT      NOT NULL REFERENCES voice_messages (id) ON DELETE CASCADE,
    old_text  TEXT        NOT NULL,
    new_text  TEXT        NOT NULL,
    edited_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX transcript_edits_voice_idx ON transcript_edits (voice_id, edited_at);

-- Настройки бота (например, reference_date — выбранный рабочий день).
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
