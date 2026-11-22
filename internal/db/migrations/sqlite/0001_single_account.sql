CREATE TABLE whatsapp_links (id INTEGER PRIMARY KEY CHECK (id = 1), account_jid TEXT NOT NULL, jid TEXT);

CREATE TABLE chats (
    jid               TEXT NOT NULL,
    name              TEXT,
    last_message_time TIMESTAMP,
    PRIMARY KEY (jid)
);

CREATE TABLE messages (
    id              TEXT NOT NULL,
    chat_jid        TEXT NOT NULL,
    sender          TEXT,
    content         TEXT,
    timestamp       TIMESTAMP NOT NULL,
    is_from_me      BOOLEAN NOT NULL DEFAULT FALSE,
    media_type      TEXT,
    filename        TEXT,
    url             TEXT,
    media_key       BLOB,
    file_sha256     BLOB,
    file_enc_sha256 BLOB,
    file_length     INTEGER,
    PRIMARY KEY (id, chat_jid),
    FOREIGN KEY (chat_jid) REFERENCES chats(jid) ON DELETE CASCADE
);

CREATE INDEX messages_chat_ts_idx ON messages (chat_jid, timestamp);
CREATE INDEX messages_ts_idx ON messages (timestamp);
