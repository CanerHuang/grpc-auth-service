# 錯誤回應

所有 RPC 的錯誤都集中在 `internal/api/authServer/handler.go` 的 `toStatusError`，把 service 層的 sentinel error 轉成對應的 gRPC status code 後回給 client。本文件說明對照關係、各 RPC 可能出現的錯誤，以及訊息揭露原則。

## 設計原則

- **集中轉換**：service 層只回 sentinel error（`internal/service/main.go`）；handler 一律用 `errors.Is` 比對後轉成 gRPC code，不在各 handler 內各自決定。
- **未對應 = Internal**：任何沒被 `toStatusError` 比對到的 error 都會落到 `codes.Internal`，並以 `error` level 記錄一筆 log，對外只回 generic 的 `internal server error`（不洩漏內部細節）。
- **訊息揭露分級**：
  - 認證類（`Unauthenticated`）一律回模糊的固定訊息，避免帳號列舉。
  - 參數類（`InvalidArgument`）會帶出具體原因（例如「username must be between 3 and 32 characters」），方便呼叫端修正。

## gRPC code 對照表

| gRPC code | sentinel error | 對外訊息 | 意義 |
|---|---|---|---|
| `Unauthenticated` | `ErrInvalidCredentials` | `invalid credentials` | 登入失敗（查無使用者 / 密碼錯 / 帳號停用，刻意不分辨）|
| `Unauthenticated` | `ErrInvalidToken` | `invalid token` | access / refresh token 無效、過期、已撤銷，或 refresh 時帳號已停用 |
| `Unauthenticated` | （handler 直接產生）| `missing metadata` / `missing authorization metadata` / `invalid authorization scheme` / `missing bearer token` | `authorization` metadata 缺漏或格式錯 |
| `InvalidArgument` | `ErrInvalidArgument` | `invalid argument: <細節>` | 輸入驗證失敗（username / display_name / roles / keyword / page_token / user_id 為 0）|
| `InvalidArgument` | `ErrInvalidPassword` | `invalid password: <細節>` | 密碼不符合複雜度規則 |
| `InvalidArgument` | `ErrInvalidUpdateMask` | `invalid update mask` | `update_mask` 為空、含未知欄位，或未帶任何可更新欄位 |
| `PermissionDenied` | `ErrPermissionDenied` | `permission denied` | 缺少所需 permission |
| `PermissionDenied` | `ErrForbiddenFieldUpdate` | `forbidden field update` | 嘗試更新沒有權限修改的欄位（roles / enabled）|
| `FailedPrecondition` | `ErrForbiddenSelfAction` | `forbidden action on self` | 禁止對自己執行的操作（改自己的 roles / enabled、刪除自己）|
| `ResourceExhausted` | `ErrUserLimitExceeded` | `user limit exceeded` | 使用者數已達 `auth.max_users` |
| `NotFound` | `sqlite.ErrUserNotFound` | `user not found` | 指定的使用者不存在 |
| `AlreadyExists` | `sqlite.ErrUsernameExists` | `username already exists` | 建立使用者時 username 重複 |
| `Internal` | （fallback）| `internal server error` | 未預期錯誤（DB 故障等），細節僅記於 server log |

## 各 RPC 可能回傳的錯誤

需要授權的 RPC（除 `Login` / `VerifyToken` / `RefreshToken` / `Logout` / `VersionGet` 外）一律可能因 `authorization` metadata 問題回 `Unauthenticated`，下表不再重複列出。

| RPC | 可能的錯誤 |
|---|---|
| `Login` | `Unauthenticated`（憑證錯誤）、`InvalidArgument`（username 格式）|
| `VerifyToken` | `Unauthenticated`（token 無效）|
| `RefreshToken` | `Unauthenticated`（token 無效 / 過期 / 已撤銷 / 帳號停用）|
| `Logout` | 冪等，正常情況恆成功（僅 DB 故障時 `Internal`）|
| `GetProfile` | `InvalidArgument`（user_id 為 0）、`PermissionDenied`（非本人且無 `user.read`）、`NotFound` |
| `CreateUser` | `PermissionDenied`（無 `user.create`）、`ResourceExhausted`（超過上限）、`InvalidArgument`（username / display_name / roles / password）、`AlreadyExists` |
| `UpdateUser` | `InvalidArgument`（user_id 為 0 / display_name / roles / update_mask / password）、`PermissionDenied`（無權改他人或無權改該欄位）、`FailedPrecondition`（改自己的 roles / enabled）、`NotFound` |
| `DeleteUser` | `InvalidArgument`（user_id 為 0）、`FailedPrecondition`（刪除自己）、`PermissionDenied`（無 `user.delete`）、`NotFound` |
| `ListUsers` | `InvalidArgument`（keyword 過長 / page_token 非法）|
| `CountUsers` | `InvalidArgument`（keyword 過長）|
| `ListRoles` | `PermissionDenied`（無 `user.read`）|
| `GetAuthSettings` | `PermissionDenied`（無 `settings.read`）|
| `UpdateAuthSettings` | `PermissionDenied`（無 `settings.update`）|
| `VersionGet` | 無（不需授權，恆成功）|

## 帳號停用的揭露策略

`Login` 與 `RefreshToken` 對「帳號已停用」採**一致的不揭露策略**：

- `Login` 遇停用帳號回 `ErrInvalidCredentials`，與「查無使用者 / 密碼錯」無法區分。
- `RefreshToken` 遇停用帳號回 `ErrInvalidToken`，與「token 無效 / 過期」無法區分。

兩者都不會明確告知「帳號已被停用」，避免向呼叫端洩漏帳號狀態。
