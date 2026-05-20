# grpc-auth-service

`grpc-auth-service` 提供使用者帳號、密碼驗證、JWT access token 簽發、refresh token 輪替與使用者管理等功能。對外採用 gRPC，服務介面定義於 [`proto/v1/auth.proto`](proto/v1/auth.proto)，編譯後的執行檔為 `authd`。

## 文件

- [doc/database.md](doc/database.md)：SQLite 資料模型、Token 策略與 gRPC 對應說明

## 專案結構

```text
grpc-auth-service/
├── main.go                     # 啟動流程：載入設定、開 SQLite、啟動 gRPC server
├── config/
│   ├── authd.toml              # 啟動期設定檔（read-only）
│   └── authd-settings.toml     # 執行期可變設定（由 gRPC 更新）
├── proto/v1/                   # gRPC 介面定義 (auth.proto)
├── internal/
│   ├── api/authServer/         # gRPC server 實作與 protobuf <-> service 轉換
│   ├── config/                 # authd.toml 嚴格載入 + authd-settings.toml 持久化
│   └── service/                # Auth 核心業務邏輯
└── pkg/
    ├── database/sqlite/        # SQLite schema 與 repository
    ├── grpc/auth/              # protoc 生成碼
    └── version/                # 版本資訊
```

## 建置與執行

建置：

```bash
go build -o authd .
```

執行：

```bash
./authd                                       # 讀取 <exe-dir>/config/authd.toml 與 <exe-dir>/config/authd-settings.toml
./authd -config /etc/authd/authd.toml \
        -settings /var/lib/authd/authd-settings.toml
```

- `-config`：啟動期設定檔，必須存在且所有欄位需通過驗證；任何缺漏或不合理的值會 fatal exit。
- `-settings`：執行期可變設定檔；不存在時會以預設值（`refresh_token_extend_on_refresh = true`）自動建立。

## 佈署 Layout

`build.sh` 產出的 `authd.tar.gz` 解開後即符合 `authd` 的預設路徑解析（設定檔位於 exe 同目錄底下的 `config/`），建議佈署到 `/opt/authd/`：

```text
/opt/authd/
├── authd                          # build.sh 產出的 binary
├── config/
│   ├── authd.toml                 # 啟動期設定（由 authd.toml.example 改名）
│   └── authd-settings.toml        # 執行期設定（首次啟動會自動建立）
└── data/
    └── authd.db                   # sqlite_path = 'data/authd.db' 為相對於 CWD
```

由於 `storage.sqlite_path` 預設為相對路徑 `data/authd.db`，啟動時的 CWD 需指向 `/opt/authd/`（systemd 用 `WorkingDirectory=`，手動跑就 `cd /opt/authd && ./authd`）。

systemd 單位檔範例見 [`systemd/authd.service`](systemd/authd.service)，安裝步驟：

```bash
# 1. 建立執行使用者（不允許登入）
useradd --system --home /opt/authd --shell /usr/sbin/nologin authd

# 2. 解開 tarball、改名 .example、設定權限
tar -xzf authd.tar.gz -C /opt
mv /opt/authd/config/authd.toml.example          /opt/authd/config/authd.toml
mv /opt/authd/config/authd-settings.toml.example /opt/authd/config/authd-settings.toml
mkdir -p /opt/authd/data
chown -R authd:authd /opt/authd

# 3. 安裝並啟動 systemd unit
cp systemd/authd.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now authd
```

## 設定檔

### `authd.toml`（啟動期設定，read-only）

所有欄位皆為必填，啟動時嚴格驗證。範例見 [`authd.toml`](config/authd.toml)。

```toml
[server]
# gRPC 監聽位址，格式為 host:port。host 留空表示綁定所有介面。
listen_address = '127.0.0.1:30052'

[storage]
# SQLite 資料庫檔案路徑；若所在目錄不存在，sqlite driver 會嘗試建立。
sqlite_path = 'data/authd.db'
# SQLite busy_timeout，遇到 lock 時最多等待的時間（time.ParseDuration 格式）。
busy_timeout = '5s'

[auth]
# JWT 簽發者，會寫入 access token 的 `iss` claim。
issuer = 'auth-service'
# access token 有效時間（≥ 1s）。
access_token_ttl = '5m'
# refresh token 有效時間（≥ 1s，且須 ≥ access_token_ttl）。
refresh_token_ttl = '30m'
# 系統允許的最大使用者數，CreateUser 超過會回 ResourceExhausted。
max_users = 20
# HS256 簽章金鑰；正式環境請改為高熵亂數，勿沿用範例值。
signing_key = 'my_secret_signing_key'
# 首次啟動且 users 表為空時，會自動建立的管理員帳號。
bootstrap_admin_username = 'admin'
bootstrap_admin_password = 'Admin123!'
bootstrap_admin_display_name = 'Admin'
bootstrap_admin_roles = ['admin']
```

主要驗證規則：

- `server.listen_address` 必須是合法的 `host:port`
- `storage.sqlite_path` 非空、`storage.busy_timeout` > 0
- `auth.access_token_ttl` / `auth.refresh_token_ttl` ≥ 1s，且 refresh ≥ access
- `auth.max_users` > 0
- `auth.signing_key`、`auth.issuer`、`auth.bootstrap_admin_*` 全部非空

### `authd-settings.toml`（執行期設定，可由 gRPC 更新）

存放可在執行期動態調整的開關，目前僅 `refresh_token_extend_on_refresh`。透過 gRPC `UpdateAuthSettings` 寫入後會立即落盤；首次啟動若檔案不存在，會以下列預設值自動建立。範例見 [`authd-settings.toml`](config/authd-settings.toml)。

```toml
# refresh token 旋轉時是否延長到期時間。
#   true  — 每次 RefreshToken 都把新 session 的有效期重設為 refresh_token_ttl，
#           只要使用者持續活動，session 不會過期。
#   false — 旋轉時沿用原 session 的 ExpiresAt，session 一旦發出就有固定壽命。
refresh_token_extend_on_refresh = true
```

## 以 grpcurl 呼叫 API

server 預設未啟用 reflection，因此每次呼叫都要帶 `-proto proto/v1/auth.proto`（或 `-import-path proto/v1 -proto auth.proto`）；尚未配 TLS，所以加 `-plaintext`。所有需要授權的 RPC 都靠 `authorization: Bearer <access_token>` metadata。

下面範例均假設工作目錄為專案根、server 監聽 `127.0.0.1:30052`。

### 登入並取得 access token

```bash
grpcurl -plaintext -proto proto/v1/auth.proto \
  -d '{"username":"admin","password":"Admin123!"}' \
  127.0.0.1:30052 auth.api.v1.AuthAPI/Login
```

把 token 存進環境變數方便後續呼叫：

```bash
TOKENS=$(grpcurl -plaintext -proto proto/v1/auth.proto \
  -d '{"username":"admin","password":"Admin123!"}' \
  127.0.0.1:30052 auth.api.v1.AuthAPI/Login)
ACCESS=$(echo "$TOKENS"  | jq -r .accessToken)
REFRESH=$(echo "$TOKENS" | jq -r .refreshToken)
```

### 已授權呼叫

```bash
# 取得當前使用者資料（user_id 從 Login response 拿）
grpcurl -plaintext -proto proto/v1/auth.proto \
  -H "authorization: Bearer ${ACCESS}" \
  -d '{"user_id":1}' \
  127.0.0.1:30052 auth.api.v1.AuthAPI/GetProfile

# 列出使用者
grpcurl -plaintext -proto proto/v1/auth.proto \
  -H "authorization: Bearer ${ACCESS}" \
  -d '{"page_size":50}' \
  127.0.0.1:30052 auth.api.v1.AuthAPI/ListUsers

# 建立使用者
grpcurl -plaintext -proto proto/v1/auth.proto \
  -H "authorization: Bearer ${ACCESS}" \
  -d '{"username":"alice","password":"Alice@123","display_name":"Alice","roles":["user"],"enabled":true}' \
  127.0.0.1:30052 auth.api.v1.AuthAPI/CreateUser

# 修改使用者（搭配 update_mask 指定要寫的欄位）
grpcurl -plaintext -proto proto/v1/auth.proto \
  -H "authorization: Bearer ${ACCESS}" \
  -d '{"user_id":2,"update_mask":{"paths":["display_name","enabled"]},"display_name":"Alice C.","enabled":false}' \
  127.0.0.1:30052 auth.api.v1.AuthAPI/UpdateUser
```

### Refresh 與 Logout

```bash
# 換新的 access token（同時旋轉 refresh token）
grpcurl -plaintext -proto proto/v1/auth.proto \
  -d "{\"refresh_token\":\"${REFRESH}\"}" \
  127.0.0.1:30052 auth.api.v1.AuthAPI/RefreshToken

# 撤銷 refresh token session
grpcurl -plaintext -proto proto/v1/auth.proto \
  -d "{\"refresh_token\":\"${REFRESH}\"}" \
  127.0.0.1:30052 auth.api.v1.AuthAPI/Logout
```

### 版本資訊

```bash
# 不需要 authorization metadata，回傳 build.sh -ldflags 注入的版本資訊
grpcurl -plaintext -proto proto/v1/auth.proto \
  127.0.0.1:30052 auth.api.v1.AuthAPI/VersionGet
```

### 讀寫執行期設定

```bash
# 讀取目前的 authd-settings.toml 值
grpcurl -plaintext -proto proto/v1/auth.proto \
  -H "authorization: Bearer ${ACCESS}" \
  127.0.0.1:30052 auth.api.v1.AuthAPI/GetAuthSettings

# 關閉 refresh token 旋轉延長（會立即落盤至 authd-settings.toml）
grpcurl -plaintext -proto proto/v1/auth.proto \
  -H "authorization: Bearer ${ACCESS}" \
  -d '{"update_mask":{"paths":["extend_refresh_token_on_refresh"]},"extend_refresh_token_on_refresh":false}' \
  127.0.0.1:30052 auth.api.v1.AuthAPI/UpdateAuthSettings
```

## Protobuf 編譯

於專案根目錄執行，生成碼會輸出至 `pkg/grpc/auth/`。

Windows：

```bash
protoc -I proto/v1 -I "include\google\protobuf" \
  --go_out=pkg/grpc/auth/. --go_opt=paths=source_relative \
  --go-grpc_out=pkg/grpc/auth/. --go-grpc_opt=paths=source_relative \
  auth.proto
```

Linux：

```bash
protoc -I proto/v1 -I /usr/include \
  --go_out=pkg/grpc/auth/. --go_opt=paths=source_relative \
  --go-grpc_out=pkg/grpc/auth/. --go-grpc_opt=paths=source_relative \
  auth.proto
```
