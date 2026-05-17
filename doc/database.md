# 資料庫設計

帳號資料以 SQLite 儲存，以下為資料表結構與 Token 策略說明。

## SQLite 資料模型

### users

- `user_id TEXT PRIMARY KEY`
- `username TEXT NOT NULL UNIQUE`
- `password_hash TEXT NOT NULL`
- `display_name TEXT NOT NULL`
- `enabled INTEGER NOT NULL`
- `created_at TEXT NOT NULL`
- `updated_at TEXT NOT NULL`
- `last_login_at TEXT`

### user_roles

- `user_id TEXT NOT NULL`
- `role TEXT NOT NULL`
- `PRIMARY KEY (user_id, role)`
