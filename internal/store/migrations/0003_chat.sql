-- 0003_chat.sql: per-incident chat message history.
CREATE TABLE chat_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace  TEXT      NOT NULL,
    name       TEXT      NOT NULL,
    role       TEXT      NOT NULL,
    content    TEXT      NOT NULL,
    created_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_chat_messages_incident ON chat_messages (namespace, name, id);
