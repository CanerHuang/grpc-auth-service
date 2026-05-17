package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"authd/internal/config"
	"authd/pkg/database/sqlite"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidToken       = errors.New("invalid token")
	ErrDisabledUser       = errors.New("user disabled")
	ErrInvalidUpdateMask  = errors.New("invalid update mask")
	ErrUserLimitExceeded  = errors.New("user limit exceeded")
	ErrInvalidPassword    = errors.New("invalid password")
	// === 授權（Authorization）相關 ===

	// ErrPermissionDenied 表示通用的授權失敗（缺少所需 permission）
	ErrPermissionDenied = errors.New("permission denied")

	// ErrForbiddenSelfAction 表示禁止對自己執行的操作
	// 例如：admin 不能刪除自己、不能停用自己、不能撤銷自己最後的 admin role
	ErrForbiddenSelfAction = errors.New("forbidden action on self")

	// ErrForbiddenFieldUpdate 表示嘗試更新一個沒有權限修改的欄位
	// 例如：一般使用者嘗試修改自己的 roles / permissions / enabled
	ErrForbiddenFieldUpdate = errors.New("forbidden field update")
)

var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9._]{1,30})[a-zA-Z0-9]$`)

const defaultRefreshPurgeInterval = time.Minute

type Service struct {
	config                config.AuthConfig
	settings              *config.SettingsStore
	store                 *sqlite.Store
	parser                *jwt.Parser
	refreshMu             sync.RWMutex
	refreshSessions       map[string]refreshSession
	triggerRefreshPurgeAt int64
}

type refreshSession struct {
	UserID    uint32
	ClientID  string
	UserAgent string
	ClientIP  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type LoginInput struct {
	Username  string
	Password  string
	ClientID  string
	UserAgent string
	ClientIP  string
}

type LoginResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	User         *sqlite.User
}

type RefreshResult struct {
	RefreshToken string
	AccessToken  string
	ExpiresIn    int64
}

type CreateUserInput struct {
	Username    string
	Password    string
	DisplayName string
	Roles       []string
	Enabled     bool
}

type UpdateUserInput struct {
	UserID      uint32
	UpdateMask  *fieldmaskpb.FieldMask
	Password    string
	DisplayName string
	Roles       []string
	Enabled     bool
}

type TokenClaims struct {
	UserID   uint32   `json:"user_id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	jwt.RegisteredClaims
}

func New(cfg config.AuthConfig, settings *config.SettingsStore, store *sqlite.Store) (*Service, error) {
	if strings.TrimSpace(cfg.SigningKey) == "" {
		return nil, fmt.Errorf("auth signing key is empty")
	}
	if settings == nil {
		return nil, fmt.Errorf("auth settings store is required")
	}
	return &Service{
		config:                cfg,
		settings:              settings,
		store:                 store,
		parser:                jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name})),
		refreshSessions:       make(map[string]refreshSession),
		triggerRefreshPurgeAt: 0,
	}, nil
}

func (s *Service) EnsureBootstrapAdmin() error {
	if strings.TrimSpace(s.config.BootstrapAdminUsername) == "" || strings.TrimSpace(s.config.BootstrapAdminPassword) == "" {
		return nil
	}

	if err := ValidateUsername(s.config.BootstrapAdminUsername); err != nil {
		return fmt.Errorf("invalid bootstrap admin username: %w", err)
	}

	if err := ValidateDisplayName(s.config.BootstrapAdminDisplayName); err != nil {
		return fmt.Errorf("invalid bootstrap admin display name: %w", err)
	}

	/*
		if err := ValidatePassword(s.config.BootstrapAdminPassword); err != nil {
			return fmt.Errorf("invalid bootstrap admin password: %w", err)
		}
	*/

	passwordHash, err := hashPassword(s.config.BootstrapAdminPassword)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	count, err := s.store.CountUsers(ctx, "", false)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	roles, err := normalizeRoleNames(s.config.BootstrapAdminRoles)
	if err != nil {
		return fmt.Errorf("invalid bootstrap admin roles: %w", err)
	}

	_, err = s.store.CreateUser(ctx, sqlite.CreateUserParams{
		Username:     s.config.BootstrapAdminUsername,
		PasswordHash: passwordHash,
		DisplayName:  s.config.BootstrapAdminDisplayName,
		Roles:        roles,
		Enabled:      true,
	})
	return err
}

func (s *Service) Login(ctx context.Context, input LoginInput) (*LoginResult, error) {
	if err := ValidateUsername(input.Username); err != nil {
		return nil, fmt.Errorf("invalid username: %w", err)
	}

	user, err := s.store.GetUserByUsername(ctx, input.Username)
	if err != nil {
		if errors.Is(err, sqlite.ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	if !user.Enabled {
		return nil, ErrInvalidCredentials
	}

	accessToken, expiresAt, err := s.signAccessToken(user)
	if err != nil {
		return nil, err
	}

	refreshToken, refreshHash, err := generateOpaqueToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	s.purgeExpiredRefreshSessions(now)
	s.saveRefreshSession(refreshHash, refreshSession{
		UserID:    user.UserID,
		ClientID:  strings.TrimSpace(input.ClientID),
		UserAgent: strings.TrimSpace(input.UserAgent),
		ClientIP:  strings.TrimSpace(input.ClientIP),
		ExpiresAt: now.Add(s.config.RefreshTokenTTL.Std()),
		CreatedAt: now,
	})

	if err := s.store.UpdateLastLogin(ctx, user.UserID, time.Now().UTC()); err != nil {
		return nil, err
	}
	user, err = s.store.GetUserByID(ctx, user.UserID)
	if err != nil {
		return nil, err
	}

	return &LoginResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(time.Until(expiresAt).Seconds()),
		User:         user,
	}, nil
}

func (s *Service) VerifyToken(ctx context.Context, accessToken string) (*TokenClaims, error) {
	_ = ctx
	claims := &TokenClaims{}
	token, err := s.parser.ParseWithClaims(accessToken, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte(s.config.SigningKey), nil
	})
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	if claims.UserID == 0 {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func (s *Service) RefreshToken(ctx context.Context, refreshToken string) (*RefreshResult, error) {
	refreshHash := hashRefreshToken(refreshToken)
	now := time.Now().UTC()
	s.purgeExpiredRefreshSessions(now)
	session, ok := s.getRefreshSession(refreshHash)
	if !ok || now.After(session.ExpiresAt) {
		return nil, ErrInvalidToken
	}

	user, err := s.store.GetUserByID(ctx, session.UserID)
	if err != nil {
		return nil, err
	}
	if !user.Enabled {
		return nil, ErrDisabledUser
	}

	accessToken, expiresAt, err := s.signAccessToken(user)
	if err != nil {
		return nil, err
	}
	newRefreshToken, newRefreshHash, err := generateOpaqueToken()
	if err != nil {
		return nil, err
	}
	if err := s.revokeRefreshSession(session.UserID, refreshHash); err != nil {
		return nil, err
	}
	newRefreshExpiresAt := session.ExpiresAt
	if s.settings.RefreshTokenExtendOnRefresh() {
		newRefreshExpiresAt = now.Add(s.config.RefreshTokenTTL.Std())
	}
	s.saveRefreshSession(newRefreshHash, refreshSession{
		UserID:    session.UserID,
		ClientID:  session.ClientID,
		UserAgent: session.UserAgent,
		ClientIP:  session.ClientIP,
		ExpiresAt: newRefreshExpiresAt,
		CreatedAt: now,
	})

	return &RefreshResult{
		RefreshToken: newRefreshToken,
		AccessToken:  accessToken,
		ExpiresIn:    int64(time.Until(expiresAt).Seconds()),
	}, nil
}

// Logout 撤銷 refresh token session。refresh token 本身為 bearer credential，
// 持有即足以撤銷，不需要額外的 access token。冪等：撤銷不存在的 token 也視為成功，
// 避免回應內容洩漏「該 token 是否存在於系統」。
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	_ = ctx
	s.purgeExpiredRefreshSessions(time.Now().UTC())
	refreshHash := hashRefreshToken(refreshToken)
	s.refreshMu.Lock()
	delete(s.refreshSessions, refreshHash)
	s.refreshMu.Unlock()
	return nil
}

func (s *Service) GetProfile(ctx context.Context, userID uint32, claimsData string) (*sqlite.User, error) {
	if userID == 0 {
		return nil, ErrForbiddenSelfAction
	}
	claims, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return nil, err
	}
	if claims.UserID != userID && !ResolvePermissions(claims.Roles).Has(PermUserRead) {
		return nil, ErrPermissionDenied
	}

	return s.store.GetUserByID(ctx, userID)
}

func (s *Service) CreateUser(ctx context.Context, input CreateUserInput, claimsData string) (*sqlite.User, error) {
	claims, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return nil, err
	}
	if !ResolvePermissions(claims.Roles).Has(PermUserCreate) {
		return nil, ErrPermissionDenied
	}

	if s.config.MaxUsers > 0 {
		count, err := s.store.CountUsers(ctx, "", false)
		if err != nil {
			return nil, err
		}
		if count >= s.config.MaxUsers {
			return nil, ErrUserLimitExceeded
		}
	}

	if err := ValidateUsername(input.Username); err != nil {
		return nil, fmt.Errorf("invalid username: %w", err)
	}

	if err := ValidateDisplayName(input.DisplayName); err != nil {
		return nil, fmt.Errorf("invalid display name: %w", err)
	}

	roles, err := normalizeRoleNames(input.Roles)
	if err != nil {
		return nil, err
	}

	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return nil, err
	}
	return s.store.CreateUser(ctx, sqlite.CreateUserParams{
		Username:     input.Username,
		PasswordHash: passwordHash,
		DisplayName:  input.DisplayName,
		Roles:        roles,
		Enabled:      input.Enabled,
	})
}

func (s *Service) UpdateUser(ctx context.Context, input UpdateUserInput, claimsData string) (*sqlite.User, error) {
	if input.UserID == 0 {
		return nil, ErrForbiddenSelfAction
	}
	claims, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return nil, err
	}
	p := ResolvePermissions(claims.Roles)
	if claims.UserID != input.UserID && !p.Has(PermUserUpdate) {
		return nil, ErrPermissionDenied
	}

	params := sqlite.UpdateUserParams{UserID: input.UserID}
	for _, path := range normalizeMaskPaths(input.UpdateMask) {
		switch path {
		case "password":
			passwordHash, err := hashPassword(input.Password)
			if err != nil {
				return nil, err
			}
			params.PasswordHash = &passwordHash
		case "display_name":
			if err := ValidateDisplayName(input.DisplayName); err != nil {
				return nil, fmt.Errorf("invalid display name: %w", err)
			}
			displayName := input.DisplayName
			params.DisplayName = &displayName
		case "roles":
			if claims.UserID == input.UserID {
				return nil, ErrForbiddenSelfAction
			}
			if !p.Has(PermUserUpdate) {
				return nil, ErrForbiddenFieldUpdate
			}
			roles, err := normalizeRoleNames(input.Roles)
			if err != nil {
				return nil, err
			}
			params.Roles = &roles
		case "enabled":
			if claims.UserID == input.UserID {
				return nil, ErrForbiddenSelfAction
			}
			if !p.Has(PermUserUpdate) {
				return nil, ErrForbiddenFieldUpdate
			}
			enabled := input.Enabled
			params.Enabled = &enabled
		default:
			return nil, ErrInvalidUpdateMask
		}
	}
	if params.PasswordHash == nil && params.DisplayName == nil && params.Roles == nil && params.Enabled == nil {
		return nil, ErrInvalidUpdateMask
	}
	return s.store.UpdateUser(ctx, params)
}

func (s *Service) DeleteUser(ctx context.Context, userID uint32, claimsData string) error {
	if userID == 0 {
		return ErrForbiddenSelfAction
	}
	claims, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return err
	}
	if claims.UserID == userID || !ResolvePermissions(claims.Roles).Has(PermUserDelete) {
		return ErrPermissionDenied
	}
	return s.store.DeleteUser(ctx, userID)
}

func (s *Service) ListUsers(ctx context.Context, pageSize int32, pageToken, keyword string, enabledOnly bool, claimsData string) ([]*sqlite.User, string, error) {
	if err := validateKeyword(keyword); err != nil {
		return nil, "", err
	}
	_, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return nil, "", err
	}

	limit := int(pageSize)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset, err := decodePageToken(pageToken)
	if err != nil {
		return nil, "", ErrInvalidToken
	}

	users, err := s.store.ListUsers(ctx, sqlite.ListUsersFilter{
		Limit:       limit,
		Offset:      offset,
		Keyword:     keyword,
		EnabledOnly: enabledOnly,
	})
	if err != nil {
		return nil, "", err
	}
	nextPageToken := ""
	if len(users) == limit {
		nextPageToken = strconv.Itoa(offset + limit)
	}
	return users, nextPageToken, nil
}

func (s *Service) CountUsers(ctx context.Context, keyword string, enabledOnly bool, claimsData string) (uint32, error) {
	if err := validateKeyword(keyword); err != nil {
		return 0, err
	}
	_, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return 0, err
	}

	return s.store.CountUsers(ctx, keyword, enabledOnly)
}

func (s *Service) ListRoles(ctx context.Context, claimsData string) ([]string, error) {
	claims, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return nil, err
	}
	if !ResolvePermissions(claims.Roles).Has(PermUserRead) {
		return nil, ErrPermissionDenied
	}

	roles := make([]string, 0, len(rolePermissions))
	for role := range rolePermissions {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles, nil
}

func (s *Service) GetAuthSettings(ctx context.Context, claimsData string) (bool, error) {
	claims, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return false, err
	}
	if !ResolvePermissions(claims.Roles).Has(PermSettingsRead) {
		return false, ErrPermissionDenied
	}

	return s.settings.RefreshTokenExtendOnRefresh(), nil
}

func (s *Service) UpdateAuthSettings(ctx context.Context, extendRefreshTokenOnRefresh bool, claimsData string) (bool, error) {
	claims, err := s.VerifyToken(ctx, claimsData)
	if err != nil {
		return false, err
	}
	if !ResolvePermissions(claims.Roles).Has(PermSettingsUpdate) {
		return false, ErrPermissionDenied
	}

	if err := s.settings.SetRefreshTokenExtendOnRefresh(extendRefreshTokenOnRefresh); err != nil {
		return false, fmt.Errorf("persist auth settings: %w", err)
	}
	return s.settings.RefreshTokenExtendOnRefresh(), nil
}

func (s *Service) signAccessToken(user *sqlite.User) (string, time.Time, error) {
	expiresAt := time.Now().UTC().Add(s.config.AccessTokenTTL.Std())
	claims := TokenClaims{
		UserID:   user.UserID,
		Username: user.Username,
		Roles:    append([]string(nil), user.Roles...),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.config.Issuer,
			Subject:   strconv.FormatUint(uint64(user.UserID), 10),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
		},
	}
	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.config.SigningKey))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token failed: %w", err)
	}
	return accessToken, expiresAt, nil
}

func (s *Service) saveRefreshSession(tokenHash string, session refreshSession) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.refreshSessions[tokenHash] = session
}

func (s *Service) getRefreshSession(tokenHash string) (refreshSession, bool) {
	s.refreshMu.RLock()
	defer s.refreshMu.RUnlock()
	session, ok := s.refreshSessions[tokenHash]
	return session, ok
}

func (s *Service) revokeRefreshSession(userId uint32, tokenHash string) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	refresh, ok := s.refreshSessions[tokenHash]
	if !ok {
		return ErrInvalidToken
	}
	if refresh.UserID != userId {
		return ErrInvalidToken
	}
	delete(s.refreshSessions, tokenHash)
	return nil
}

func (s *Service) purgeExpiredRefreshSessions(now time.Time) {
	nowUnixNano := now.UnixNano()
	if s.triggerRefreshPurgeAt > nowUnixNano {
		return
	}
	s.triggerRefreshPurgeAt = nowUnixNano + defaultRefreshPurgeInterval.Nanoseconds()

	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	for tokenHash, session := range s.refreshSessions {
		if now.After(session.ExpiresAt) {
			delete(s.refreshSessions, tokenHash)
		}
	}
}

func hashPassword(password string) (string, error) {
	cleanPassword := strings.TrimSpace(password)
	if err := ValidatePassword(cleanPassword); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPassword, err)
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(cleanPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("%w: hash password failed: %w", ErrInvalidPassword, err)
	}
	return string(hashed), nil
}

func generateOpaqueToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate refresh token failed: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, hashRefreshToken(token), nil
}

func hashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func normalizeMaskPaths(mask *fieldmaskpb.FieldMask) []string {
	if mask == nil {
		return nil
	}
	paths := make([]string, 0, len(mask.Paths))
	for _, path := range mask.Paths {
		clean := strings.TrimSpace(path)
		if clean == "" {
			continue
		}
		paths = append(paths, clean)
	}
	return paths
}

func normalizeRoleNames(roles []string) ([]string, error) {
	roleSet := make(map[string]struct{})
	for _, role := range roles {
		clean := strings.TrimSpace(role)
		if clean == "" {
			continue
		}
		roleSet[clean] = struct{}{}
	}
	result := make([]string, 0, len(roleSet))
	for role := range roleSet {
		if _, exists := rolePermissions[role]; !exists {
			return nil, fmt.Errorf("undefined role: %s", role)
		}
		result = append(result, role)
	}
	sort.Strings(result)
	return result, nil
}

func decodePageToken(token string) (int, error) {
	clean := strings.TrimSpace(token)
	if clean == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(clean)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid page token")
	}
	return offset, nil
}

func ValidateUsername(username string) error {
	if len(username) < 3 || len(username) > 32 {
		return errors.New("username must be between 3 and 32 characters")
	}

	if !usernameRegex.MatchString(username) {
		return errors.New("username contains invalid characters or format")
	}

	return nil
}

// ValidateDisplayName 允許 unicode letter/digit（含中日韓等），加上 `.` 與 `_` 兩個符號；
// 不允許任何 unicode 空白（含 NBSP、零寬等）與控制字元；開頭與結尾必須是 letter 或 digit。
// 長度以 rune 數計，限制 1-10。
func ValidateDisplayName(displayName string) error {
	runes := []rune(displayName)
	n := len(runes)
	if n < 1 || n > 10 {
		return errors.New("display name must be between 1 and 10 characters")
	}

	for idx, r := range runes {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			// 任何位置都可以
		case r == '.' || r == '_':
			if idx == 0 || idx == n-1 {
				return errors.New("display name must not start or end with '.' or '_'")
			}
		case unicode.IsSpace(r):
			return errors.New("display name must not contain whitespace")
		case unicode.IsControl(r):
			return errors.New("display name must not contain control characters")
		default:
			return errors.New("display name contains invalid character")
		}
	}
	return nil
}

// validateKeyword 僅檢查搜尋字串長度上限；不限字元集，允許 unicode（含中日韓）與符號。
// SQL 已用參數化 query，LIKE 萬用字元由 sqlite 層 escape，無 injection 風險。
func validateKeyword(keyword string) error {
	k := strings.TrimSpace(keyword)
	if k == "" {
		return nil
	}
	if utf8.RuneCountInString(k) > 100 {
		return errors.New("keyword must be at most 100 characters")
	}
	return nil
}

func ValidatePassword(password string) error {
	if len(password) < 8 || len(password) > 72 {
		return errors.New("password must be between 8 and 72 characters")
	}

	var hasUpper bool
	var hasLower bool
	var hasDigit bool
	var hasSpecial bool

	for _, r := range password {
		if !isAllowedPasswordRune(r) {
			return errors.New("password contains invalid character")
		}

		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasSpecial = true
		}
	}

	if !hasUpper {
		return errors.New("password must contain at least one uppercase letter")
	}
	if !hasLower {
		return errors.New("password must contain at least one lowercase letter")
	}
	if !hasDigit {
		return errors.New("password must contain at least one number")
	}
	if !hasSpecial {
		return errors.New("password must contain at least one special character")
	}

	return nil
}

func isAllowedPasswordRune(r rune) bool {
	return r >= 0x21 && r <= 0x7E
}
