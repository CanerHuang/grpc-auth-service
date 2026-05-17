package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func setupDB(t *testing.T) (*Store, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sqlite-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, "auth.db")

	store, err := Open(dbPath, 5*time.Second)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	return store, func() {
		store.Close()
		os.RemoveAll(dir)
	}
}

func ptrString(s string) *string    { return &s }
func ptrBool(b bool) *bool          { return &b }
func ptrSlice(s []string) *[]string { return &s }

func TestOpen(t *testing.T) {
	t.Run("Empty path", func(t *testing.T) {
		_, err := Open("   ", 5*time.Second)
		if err == nil || err.Error() != "sqlite path is empty" {
			t.Errorf("expected empty path error, got %v", err)
		}
	})

	t.Run("Valid path", func(t *testing.T) {
		dir, _ := os.MkdirTemp("", "sqlite-test-*")
		defer os.RemoveAll(dir)
		dbPath := filepath.Join(dir, "valid.db")
		store, err := Open(dbPath, 5*time.Second)
		if err != nil {
			t.Fatalf("failed to open valid db: %v", err)
		}
		defer store.Close()
	})
}

func TestUserCRUD(t *testing.T) {
	store, cleanup := setupDB(t)
	defer cleanup()

	ctx := context.Background()

	// 1. CreateUser
	params := CreateUserParams{
		Username:     "testuser",
		PasswordHash: "hash123",
		DisplayName:  "Test User",
		Roles:        []string{"user", "admin", "user"}, // Test dedupe and sort
		Enabled:      true,
	}

	user, err := store.CreateUser(ctx, params)
	if err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	if user.Username != params.Username {
		t.Errorf("expected username %s, got %s", params.Username, user.Username)
	}
	if len(user.Roles) != 2 || user.Roles[0] != "admin" || user.Roles[1] != "user" {
		t.Errorf("expected roles [admin user] (sorted by DB), got %v", user.Roles)
	}

	// 2. Duplicate CreateUser
	_, err = store.CreateUser(ctx, params)
	if !errors.Is(err, ErrUsernameExists) {
		t.Errorf("expected ErrUsernameExists, got %v", err)
	}

	// 3. GetUserByID
	fetchedUser, err := store.GetUserByID(ctx, user.UserID)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}
	if fetchedUser.Username != user.Username {
		t.Errorf("expected username %s, got %s", user.Username, fetchedUser.Username)
	}

	// 4. GetUserByUsername
	fetchedUser2, err := store.GetUserByUsername(ctx, "testuser")
	if err != nil {
		t.Fatalf("GetUserByUsername failed: %v", err)
	}
	if fetchedUser2.UserID != user.UserID {
		t.Errorf("expected ID %d, got %d", user.UserID, fetchedUser2.UserID)
	}

	// 5. UpdateUser
	updateParams := UpdateUserParams{
		UserID:      user.UserID,
		DisplayName: ptrString("Updated User"),
		Enabled:     ptrBool(false),
		Roles:       ptrSlice([]string{"guest"}),
	}

	updatedUser, err := store.UpdateUser(ctx, updateParams)
	if err != nil {
		t.Fatalf("UpdateUser failed: %v", err)
	}
	if updatedUser.DisplayName != "Updated User" {
		t.Errorf("expected DisplayName %s, got %s", "Updated User", updatedUser.DisplayName)
	}
	if updatedUser.Enabled != false {
		t.Errorf("expected Enabled %v, got %v", false, updatedUser.Enabled)
	}
	if len(updatedUser.Roles) != 1 || updatedUser.Roles[0] != "guest" {
		t.Errorf("expected roles [guest], got %v", updatedUser.Roles)
	}

	// 7. Hard DeleteUser
	err = store.DeleteUser(ctx, user.UserID)
	if err != nil {
		t.Fatalf("Hard DeleteUser failed: %v", err)
	}

	_, err = store.GetUserByID(ctx, user.UserID)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}

	// 8. Delete Non-Existent User
	err = store.DeleteUser(ctx, 9999)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

func TestListAndCountUsers(t *testing.T) {
	store, cleanup := setupDB(t)
	defer cleanup()

	ctx := context.Background()

	// Setup users
	users := []CreateUserParams{
		{Username: "alice", DisplayName: "Alice Smith", Enabled: true},
		{Username: "bob", DisplayName: "Bob Jones", Enabled: true},
		{Username: "charlie", DisplayName: "Charlie Brown", Enabled: false},
	}

	for _, u := range users {
		_, err := store.CreateUser(ctx, u)
		if err != nil {
			t.Fatalf("failed to create user %s: %v", u.Username, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// List all
	list, err := store.ListUsers(ctx, ListUsersFilter{})
	if err != nil {
		t.Fatalf("ListUsers failed: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 users, got %d", len(list))
	}
	// Verify order (created_at DESC)
	if list[0].Username != "charlie" || list[2].Username != "alice" {
		t.Errorf("expected descending order, got %s then %s", list[0].Username, list[2].Username)
	}

	// Count all
	count, err := store.CountUsers(ctx, "", false)
	if err != nil {
		t.Fatalf("CountUsers failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count 3, got %d", count)
	}

	// Filter by keyword (matches username or display_name)
	list, err = store.ListUsers(ctx, ListUsersFilter{Keyword: "Smith"})
	if err != nil {
		t.Fatalf("ListUsers with keyword failed: %v", err)
	}
	if len(list) != 1 || list[0].Username != "alice" {
		t.Errorf("expected 1 user (alice), got %v", len(list))
	}

	count, err = store.CountUsers(ctx, "bob", false)
	if err != nil {
		t.Fatalf("CountUsers with keyword failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected count 1, got %d", count)
	}

	// Filter enabled only
	list, err = store.ListUsers(ctx, ListUsersFilter{EnabledOnly: true})
	if err != nil {
		t.Fatalf("ListUsers enabled only failed: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 enabled users, got %d", len(list))
	}

	count, err = store.CountUsers(ctx, "", true)
	if err != nil {
		t.Fatalf("CountUsers enabled only failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected count 2, got %d", count)
	}

	// Pagination (Limit & Offset)
	list, err = store.ListUsers(ctx, ListUsersFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("ListUsers with limit/offset failed: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 user for pagination, got %d", len(list))
	}
	if list[0].Username != "bob" {
		t.Errorf("expected bob, got %s", list[0].Username)
	}
}

func TestKeywordLikeEscape(t *testing.T) {
	store, cleanup := setupDB(t)
	defer cleanup()
	ctx := context.Background()

	// 建立含 LIKE 萬用字元（% _）與一般字串的 user，驗證 escape 後搜尋只命中字面相符者。
	candidates := []CreateUserParams{
		{Username: "user1", DisplayName: "100% Done", Enabled: true},
		{Username: "user2", DisplayName: "1000 Done", Enabled: true},
		{Username: "user3", DisplayName: "foo_bar", Enabled: true},
		{Username: "user4", DisplayName: "fooXbar", Enabled: true},
		{Username: "user5", DisplayName: "Plain Name", Enabled: true},
	}
	for _, u := range candidates {
		if _, err := store.CreateUser(ctx, u); err != nil {
			t.Fatalf("create %s: %v", u.Username, err)
		}
	}

	cases := []struct {
		name    string
		keyword string
		wantHit []string // display names that should match
	}{
		{"percent literal", "100%", []string{"100% Done"}},
		{"underscore literal", "foo_bar", []string{"foo_bar"}},
		{"plain substring", "Done", []string{"100% Done", "1000 Done"}},
		{"backslash literal", `\`, nil}, // 沒有 user 含 backslash
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list, err := store.ListUsers(ctx, ListUsersFilter{Keyword: tc.keyword})
			if err != nil {
				t.Fatalf("ListUsers: %v", err)
			}
			got := make(map[string]bool, len(list))
			for _, u := range list {
				got[u.DisplayName] = true
			}
			if len(got) != len(tc.wantHit) {
				t.Errorf("keyword %q: got %d hits %v, want %d %v", tc.keyword, len(got), got, len(tc.wantHit), tc.wantHit)
			}
			for _, want := range tc.wantHit {
				if !got[want] {
					t.Errorf("keyword %q: missing hit %q (got %v)", tc.keyword, want, got)
				}
			}

			count, err := store.CountUsers(ctx, tc.keyword, false)
			if err != nil {
				t.Fatalf("CountUsers: %v", err)
			}
			if int(count) != len(tc.wantHit) {
				t.Errorf("CountUsers keyword %q: got %d, want %d", tc.keyword, count, len(tc.wantHit))
			}
		})
	}
}

func TestUpdateLastLogin(t *testing.T) {
	store, cleanup := setupDB(t)
	defer cleanup()

	ctx := context.Background()

	user, _ := store.CreateUser(ctx, CreateUserParams{Username: "loginuser"})
	if user.LastLoginAt != nil {
		t.Errorf("expected initial LastLoginAt to be nil")
	}

	now := time.Now().UTC()
	err := store.UpdateLastLogin(ctx, user.UserID, now)
	if err != nil {
		t.Fatalf("UpdateLastLogin failed: %v", err)
	}

	updated, _ := store.GetUserByID(ctx, user.UserID)
	if updated.LastLoginAt == nil {
		t.Errorf("expected LastLoginAt to be set")
	} else if updated.LastLoginAt.Format(time.RFC3339) != now.Format(time.RFC3339) {
		t.Errorf("expected time %v, got %v", now.Format(time.RFC3339), updated.LastLoginAt.Format(time.RFC3339))
	}
}

func TestUtilityFunctions(t *testing.T) {
	// dedupe
	in := []string{"a", "b", " a ", "", "b", "c"}
	out := dedupe(in)
	expected := []string{"a", "b", "c"}
	if !reflect.DeepEqual(out, expected) {
		t.Errorf("dedupe: expected %v, got %v", expected, out)
	}

	// boolToInt
	if boolToInt(true) != 1 {
		t.Errorf("expected 1")
	}
	if boolToInt(false) != 0 {
		t.Errorf("expected 0")
	}

	// time helpers
	now := time.Now().UTC()
	if unixMillisToTime(timeToUnixMillis(now)).UnixMilli() != now.UnixMilli() {
		t.Errorf("time conversion should preserve unix milliseconds")
	}

	// int64ToUserID
	id, err := int64ToUserID(1)
	if err != nil || id != 1 {
		t.Errorf("expected 1, got %d, %v", id, err)
	}

	_, err = int64ToUserID(0)
	if err == nil {
		t.Errorf("expected error for 0")
	}

	_, err = int64ToUserID(-1)
	if err == nil {
		t.Errorf("expected error for -1")
	}
}
