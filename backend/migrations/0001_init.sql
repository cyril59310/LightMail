-- LightMail initial schema (SQLite).
-- Timestamps are Unix seconds (UTC).

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    is_admin      INTEGER NOT NULL DEFAULT 1,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,           -- SHA-256 of the cookie token
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token   TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,           -- absolute expiry
    user_agent   TEXT NOT NULL DEFAULT '',
    ip           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE settings (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key     TEXT NOT NULL,
    value   TEXT NOT NULL,
    PRIMARY KEY (user_id, key)
);

CREATE TABLE accounts (
    id                INTEGER PRIMARY KEY,
    user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider          TEXT NOT NULL,
    email             TEXT NOT NULL,
    display_name      TEXT NOT NULL DEFAULT '',
    sender_name       TEXT NOT NULL DEFAULT '',
    color             TEXT NOT NULL DEFAULT '',
    imap_host         TEXT NOT NULL,
    imap_port         INTEGER NOT NULL,
    imap_security     TEXT NOT NULL,          -- tls | starttls
    smtp_host         TEXT NOT NULL,
    smtp_port         INTEGER NOT NULL,
    smtp_security     TEXT NOT NULL,          -- tls | starttls
    auth_type         TEXT NOT NULL,          -- password | oauth2
    username          TEXT NOT NULL,
    password_enc      TEXT NOT NULL DEFAULT '',
    oauth_access_enc  TEXT NOT NULL DEFAULT '',
    oauth_refresh_enc TEXT NOT NULL DEFAULT '',
    oauth_expiry      INTEGER NOT NULL DEFAULT 0,
    oauth_scope       TEXT NOT NULL DEFAULT '',
    oauth_tenant      TEXT NOT NULL DEFAULT '',
    tls_ca_pem        TEXT NOT NULL DEFAULT '',
    signature_html    TEXT NOT NULL DEFAULT '',
    is_default        INTEGER NOT NULL DEFAULT 0,
    sync_enabled      INTEGER NOT NULL DEFAULT 1,
    sync_interval_sec INTEGER NOT NULL DEFAULT 0,
    idle_enabled      INTEGER NOT NULL DEFAULT 1,
    smtp_saves_sent   INTEGER NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'pending',
    last_error        TEXT NOT NULL DEFAULT '',
    last_error_at     INTEGER NOT NULL DEFAULT 0,
    last_sync_at      INTEGER NOT NULL DEFAULT 0,
    last_sync_ok_at   INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    UNIQUE (user_id, email, imap_host)
);
CREATE INDEX accounts_user ON accounts(user_id);

CREATE TABLE folders (
    id                INTEGER PRIMARY KEY,
    account_id        INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,           -- IMAP mailbox name (UTF-8)
    delimiter         TEXT NOT NULL DEFAULT '/',
    role              TEXT NOT NULL DEFAULT '', -- inbox|sent|drafts|trash|junk|archive|all|flagged|important
    attrs             TEXT NOT NULL DEFAULT '',
    selectable        INTEGER NOT NULL DEFAULT 1,
    subscribed        INTEGER NOT NULL DEFAULT 1,
    sync_enabled      INTEGER NOT NULL DEFAULT 1,
    uidvalidity       INTEGER NOT NULL DEFAULT 0,
    uidnext           INTEGER NOT NULL DEFAULT 0,
    highest_modseq    INTEGER NOT NULL DEFAULT 0,
    num_messages      INTEGER NOT NULL DEFAULT 0,
    min_uid           INTEGER NOT NULL DEFAULT 0,
    max_uid           INTEGER NOT NULL DEFAULT 0,
    backfill_done     INTEGER NOT NULL DEFAULT 0,
    last_flag_sync_at INTEGER NOT NULL DEFAULT 0,
    last_sync_at      INTEGER NOT NULL DEFAULT 0,
    UNIQUE (account_id, name)
);

CREATE TABLE messages (
    id              INTEGER PRIMARY KEY,
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    folder_id       INTEGER NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
    uid             INTEGER NOT NULL,
    message_id      TEXT NOT NULL DEFAULT '',
    in_reply_to     TEXT NOT NULL DEFAULT '',
    refs            TEXT NOT NULL DEFAULT '',
    thread_id       INTEGER NOT NULL DEFAULT 0,
    subject         TEXT NOT NULL DEFAULT '',
    from_name       TEXT NOT NULL DEFAULT '',
    from_addr       TEXT NOT NULL DEFAULT '',
    to_json         TEXT NOT NULL DEFAULT '[]',
    cc_json         TEXT NOT NULL DEFAULT '[]',
    bcc_json        TEXT NOT NULL DEFAULT '[]',
    reply_to_json   TEXT NOT NULL DEFAULT '[]',
    date            INTEGER NOT NULL,
    internal_date   INTEGER NOT NULL,
    size            INTEGER NOT NULL DEFAULT 0,
    seen            INTEGER NOT NULL DEFAULT 0,
    flagged         INTEGER NOT NULL DEFAULT 0,
    answered        INTEGER NOT NULL DEFAULT 0,
    draft           INTEGER NOT NULL DEFAULT 0,
    forwarded       INTEGER NOT NULL DEFAULT 0,
    keywords        TEXT NOT NULL DEFAULT '',
    has_attachments INTEGER NOT NULL DEFAULT 0,
    priority        INTEGER NOT NULL DEFAULT 0,   -- 1 high, 0 normal, -1 low
    snippet         TEXT NOT NULL DEFAULT '',
    modseq          INTEGER NOT NULL DEFAULT 0,
    structure       TEXT NOT NULL DEFAULT '',     -- JSON description of text parts
    body_fetched    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (folder_id, uid)
);
CREATE INDEX messages_folder_date  ON messages(folder_id, date DESC, id DESC);
CREATE INDEX messages_account_date ON messages(account_id, date DESC, id DESC);
CREATE INDEX messages_date         ON messages(date DESC, id DESC);
CREATE INDEX messages_thread       ON messages(account_id, thread_id);
CREATE INDEX messages_msgid        ON messages(account_id, message_id);
CREATE INDEX messages_flagged      ON messages(date DESC, id DESC) WHERE flagged = 1;
CREATE INDEX messages_unseen       ON messages(folder_id) WHERE seen = 0;

CREATE TABLE message_bodies (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    text_body  TEXT NOT NULL DEFAULT '',
    html_body  TEXT NOT NULL DEFAULT '',
    fetched_at INTEGER NOT NULL
);

CREATE TABLE attachments (
    id           INTEGER PRIMARY KEY,
    message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    part         TEXT NOT NULL,             -- IMAP section path, e.g. "2" or "1.3"
    filename     TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    encoding     TEXT NOT NULL DEFAULT '',
    size         INTEGER NOT NULL DEFAULT 0,
    content_id   TEXT NOT NULL DEFAULT '',
    inline       INTEGER NOT NULL DEFAULT 0,
    blob_sha256  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX attachments_message ON attachments(message_id);

CREATE TABLE blobs (
    sha256         TEXT PRIMARY KEY,
    size           INTEGER NOT NULL,
    created_at     INTEGER NOT NULL,
    last_access_at INTEGER NOT NULL
);

-- Full-text index (contentless: text lives in messages / message_bodies).
CREATE VIRTUAL TABLE messages_fts USING fts5(
    subject, sender, recipients, body,
    content = '', contentless_delete = 1,
    tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TRIGGER messages_fts_delete AFTER DELETE ON messages BEGIN
    DELETE FROM messages_fts WHERE rowid = old.id;
END;

CREATE TABLE uploads (
    id           TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    filename     TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size         INTEGER NOT NULL,
    blob_sha256  TEXT NOT NULL,
    created_at   INTEGER NOT NULL
);

CREATE TABLE drafts (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id      INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    mode            TEXT NOT NULL DEFAULT 'new',  -- new|reply|replyall|forward
    related_msg     INTEGER,                      -- local message replied to / forwarded
    to_json         TEXT NOT NULL DEFAULT '[]',
    cc_json         TEXT NOT NULL DEFAULT '[]',
    bcc_json        TEXT NOT NULL DEFAULT '[]',
    subject         TEXT NOT NULL DEFAULT '',
    body_html       TEXT NOT NULL DEFAULT '',
    body_text       TEXT NOT NULL DEFAULT '',
    is_html         INTEGER NOT NULL DEFAULT 1,
    uploads_json    TEXT NOT NULL DEFAULT '[]',
    keep_orig_atts  INTEGER NOT NULL DEFAULT 0,
    server_folder   INTEGER,
    server_uid      INTEGER NOT NULL DEFAULT 0,
    server_synced   INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE TABLE outbox (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id      INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    status          TEXT NOT NULL,      -- scheduled|sending|retry|sent|failed|ambiguous|canceled
    message_id_hdr  TEXT NOT NULL,
    subject         TEXT NOT NULL DEFAULT '',
    envelope_from   TEXT NOT NULL,
    rcpts_json      TEXT NOT NULL,
    raw_sha256      TEXT NOT NULL,
    related_msg     INTEGER,
    mode            TEXT NOT NULL DEFAULT 'new',
    draft_id        INTEGER,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT NOT NULL DEFAULT '',
    sent_at         INTEGER NOT NULL DEFAULT 0,
    copied_to_sent  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE INDEX outbox_due ON outbox(status, next_attempt_at);

CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    read_at    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notifications_user ON notifications(user_id, created_at DESC);

CREATE TABLE oauth_states (
    state_hash   TEXT PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_hash TEXT NOT NULL,
    provider     TEXT NOT NULL,
    verifier_enc TEXT NOT NULL,
    account_id   INTEGER,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);
