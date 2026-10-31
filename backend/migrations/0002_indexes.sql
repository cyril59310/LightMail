-- Indexes found missing by the 100k-message benchmark (see docs/TESTING.md).

-- Thread lookup of replies arriving before their parent (was a full scan
-- per inserted message, making large initial syncs quadratic).
-- (Not a partial index: SQLite cannot prove "in_reply_to = ?" implies "<> ''".)
CREATE INDEX messages_irt ON messages(account_id, in_reply_to);

-- Conversation view ordered by date.
DROP INDEX messages_thread;
CREATE INDEX messages_thread ON messages(account_id, thread_id, date);
