-- Address book: contacts saved by each user, private to that user.
CREATE TABLE contacts (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL DEFAULT '',
    email      TEXT NOT NULL,
    notes      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
-- One entry per address and user (case-insensitive).
CREATE UNIQUE INDEX contacts_user_email ON contacts(user_id, email COLLATE NOCASE);
