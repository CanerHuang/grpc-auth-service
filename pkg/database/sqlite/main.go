package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrUserNotFound   = errors.New("user not found")
	ErrUsernameExists = errors.New("username already exists")
)

type Store struct {
	db *sql.DB
}

type User struct {
	UserID       uint32
	Username     string
	PasswordHash string
	DisplayName  string
	Roles        []string
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastLoginAt  *time.Time
}

type CreateUserParams struct {
	Username     string
	PasswordHash string
	DisplayName  string
	Roles        []string
	Enabled      bool
}

type UpdateUserParams struct {
	UserID       uint32
	PasswordHash *string
	DisplayName  *string
	Roles        *[]string
	Enabled      *bool
}

type ListUsersFilter struct {
	Limit       int
	Offset      int
	Keyword     string
	EnabledOnly bool
}

func Open(path string, busyTimeout time.Duration) (*Store, error) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o755); err != nil && filepath.Dir(cleanPath) != "." {
		return nil, fmt.Errorf("create sqlite dir failed: %w", err)
	}

	pathDSN := fmt.Sprintf(
		"file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)",
		cleanPath,
		busyTimeout.Milliseconds(),
	)
	db, err := sql.Open("sqlite", pathDSN)
	if err != nil {
		return nil, fmt.Errorf("open sqlite failed: %w", err)
	}

	// SQLite 在同一時間僅允許單一寫入（writer）。限制連線數量可避免
	// 連線池中不同 connection 的 PRAGMA 設定不一致，並降低程序內部的鎖競爭。
	// 然而，資料一致性仍需透過 transaction 邊界來確保。
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			user_id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			display_name TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			last_login_at INTEGER
		);`,
		`CREATE TABLE IF NOT EXISTS user_roles (
			user_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			PRIMARY KEY (user_id, role),
			FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE CASCADE
		);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("run sqlite migration failed: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateUser(ctx context.Context, params CreateUserParams) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create user transaction failed: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	result, err := tx.ExecContext(
		ctx,
		`INSERT INTO users (username, password_hash, display_name, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		params.Username,
		params.PasswordHash,
		params.DisplayName,
		boolToInt(params.Enabled),
		timeToUnixMillis(now),
		timeToUnixMillis(now),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrUsernameExists
		}
		return nil, fmt.Errorf("insert user failed: %w", err)
	}

	createdID, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get created user id failed: %w", err)
	}
	userID, err := int64ToUserID(createdID)
	if err != nil {
		return nil, err
	}

	if err := replaceRoles(ctx, tx, userID, params.Roles); err != nil {
		return nil, err
	}

	created, err := getUser(ctx, tx, `SELECT user_id, username, password_hash, display_name, enabled, created_at, updated_at, last_login_at FROM users WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create user failed: %w", err)
	}
	return created, nil
}

func (s *Store) GetUserByID(ctx context.Context, userID uint32) (*User, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin get user transaction failed: %w", err)
	}
	defer tx.Rollback()

	user, err := getUser(ctx, tx, `SELECT user_id, username, password_hash, display_name, enabled, created_at, updated_at, last_login_at FROM users WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get user transaction failed: %w", err)
	}
	return user, nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin get user transaction failed: %w", err)
	}
	defer tx.Rollback()
	user, err := getUser(ctx, tx, `SELECT user_id, username, password_hash, display_name, enabled, created_at, updated_at, last_login_at FROM users WHERE username = ?`, username)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get user transaction failed: %w", err)
	}
	return user, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}

func getUser(ctx context.Context, q queryer, query string, arg interface{}) (*User, error) {
	var (
		user           User
		userIDRaw      int64
		enabledInt     int
		createdAtRaw   int64
		updatedAtRaw   int64
		lastLoginAtRaw sql.NullInt64
	)

	err := q.QueryRowContext(ctx, query, arg).Scan(
		&userIDRaw,
		&user.Username,
		&user.PasswordHash,
		&user.DisplayName,
		&enabledInt,
		&createdAtRaw,
		&updatedAtRaw,
		&lastLoginAtRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query user failed: %w", err)
	}
	userID, err := int64ToUserID(userIDRaw)
	if err != nil {
		return nil, err
	}
	user.UserID = userID

	user.CreatedAt = unixMillisToTime(createdAtRaw)
	user.UpdatedAt = unixMillisToTime(updatedAtRaw)
	user.Enabled = enabledInt == 1
	if lastLoginAtRaw.Valid {
		lastLoginAt := unixMillisToTime(lastLoginAtRaw.Int64)
		user.LastLoginAt = &lastLoginAt
	}

	roles, err := listValues(ctx, q, `SELECT role FROM user_roles WHERE user_id = ? ORDER BY role`, user.UserID)
	if err != nil {
		return nil, err
	}
	user.Roles = roles
	return &user, nil
}

func (s *Store) UpdateUser(ctx context.Context, params UpdateUserParams) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update user transaction failed: %w", err)
	}
	defer tx.Rollback()

	current, err := getUser(ctx, tx, `SELECT user_id, username, password_hash, display_name, enabled, created_at, updated_at, last_login_at FROM users WHERE user_id = ?`, params.UserID)
	if err != nil {
		return nil, err
	}

	passwordHash := current.PasswordHash
	if params.PasswordHash != nil {
		passwordHash = *params.PasswordHash
	}
	displayName := current.DisplayName
	if params.DisplayName != nil {
		displayName = *params.DisplayName
	}
	enabled := current.Enabled
	if params.Enabled != nil {
		enabled = *params.Enabled
	}

	_, err = tx.ExecContext(
		ctx,
		`UPDATE users SET password_hash = ?, display_name = ?, enabled = ?, updated_at = ? WHERE user_id = ?`,
		passwordHash,
		displayName,
		boolToInt(enabled),
		timeToUnixMillis(time.Now().UTC()),
		params.UserID,
	)
	if err != nil {
		return nil, fmt.Errorf("update user failed: %w", err)
	}

	if params.Roles != nil {
		if err := replaceRoles(ctx, tx, params.UserID, *params.Roles); err != nil {
			return nil, err
		}
	}

	updated, err := getUser(ctx, tx, `SELECT user_id, username, password_hash, display_name, enabled, created_at, updated_at, last_login_at FROM users WHERE user_id = ?`, params.UserID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update user failed: %w", err)
	}
	return updated, nil
}

func (s *Store) DeleteUser(ctx context.Context, userID uint32) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete user failed: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get delete rows affected failed: %w", err)
	}
	if rows == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *Store) ListUsers(ctx context.Context, filter ListUsersFilter) ([]*User, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin list users transaction failed: %w", err)
	}
	defer tx.Rollback()

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	where := []string{"1 = 1"}
	args := make([]interface{}, 0, 4)
	if strings.TrimSpace(filter.Keyword) != "" {
		where = append(where, `(username LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\')`)
		keyword := "%" + escapeLikePattern(strings.TrimSpace(filter.Keyword)) + "%"
		args = append(args, keyword, keyword)
	}
	if filter.EnabledOnly {
		where = append(where, "enabled = 1")
	}
	args = append(args, limit, offset)

	query := fmt.Sprintf(`SELECT user_id FROM users WHERE %s ORDER BY created_at DESC LIMIT ? OFFSET ?`, strings.Join(where, " AND "))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list users failed: %w", err)
	}
	defer rows.Close()

	userIDs := make([]uint32, 0, limit)
	for rows.Next() {
		var userIDRaw int64
		if err := rows.Scan(&userIDRaw); err != nil {
			return nil, fmt.Errorf("scan user id failed: %w", err)
		}
		userID, err := int64ToUserID(userIDRaw)
		if err != nil {
			return nil, err
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users failed: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close list users rows failed: %w", err)
	}

	users := make([]*User, 0, len(userIDs))
	for _, userID := range userIDs {
		user, err := getUser(ctx, tx, `SELECT user_id, username, password_hash, display_name, enabled, created_at, updated_at, last_login_at FROM users WHERE user_id = ?`, userID)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit list users transaction failed: %w", err)
	}
	return users, nil
}

func (s *Store) CountUsers(ctx context.Context, keyword string, enabledOnly bool) (uint32, error) {
	where := []string{"1 = 1"}
	args := make([]interface{}, 0, 2)
	if strings.TrimSpace(keyword) != "" {
		where = append(where, `(username LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\')`)
		likeKeyword := "%" + escapeLikePattern(strings.TrimSpace(keyword)) + "%"
		args = append(args, likeKeyword, likeKeyword)
	}
	if enabledOnly {
		where = append(where, "enabled = 1")
	}

	query := fmt.Sprintf(`SELECT COUNT(1) FROM users WHERE %s`, strings.Join(where, " AND "))
	var count uint32
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count users failed: %w", err)
	}
	return count, nil
}

func (s *Store) UpdateLastLogin(ctx context.Context, userID uint32, lastLoginAt time.Time) error {
	result, err := s.db.ExecContext(
		ctx,
		`UPDATE users SET last_login_at = ? WHERE user_id = ?`,
		timeToUnixMillis(lastLoginAt.UTC()),
		userID,
	)
	if err != nil {
		return fmt.Errorf("update last login failed: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("get update last login rows affected failed: %w", err)
	}
	if rows == 0 {
		return ErrUserNotFound
	}
	return nil
}

func listValues(ctx context.Context, q queryer, query string, userID uint32) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("query user values failed: %w", err)
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan user value failed: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user values failed: %w", err)
	}
	return values, nil
}

func replaceRoles(ctx context.Context, tx *sql.Tx, userID uint32, roles []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("clear user roles failed: %w", err)
	}
	for _, role := range dedupe(roles) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_roles (user_id, role) VALUES (?, ?)`, userID, role); err != nil {
			return fmt.Errorf("insert user role failed: %w", err)
		}
	}
	return nil
}

func timeToUnixMillis(t time.Time) int64 {
	return t.UTC().UnixMilli()
}

func unixMillisToTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		result = append(result, clean)
	}
	return result
}

func int64ToUserID(value int64) (uint32, error) {
	if value <= 0 || value > int64(^uint32(0)) {
		return 0, fmt.Errorf("invalid user id: %d", value)
	}
	return uint32(value), nil
}

// escapeLikePattern 將使用者輸入中的 LIKE 萬用字元（% _）與 escape 字元本身（\）
// 全部加上 backslash 前綴，使其在 SQL LIKE pattern 中被視為字面字元。
// 搭配 `LIKE ? ESCAPE '\'` 使用。
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLikePattern(s string) string {
	return likeEscaper.Replace(s)
}
