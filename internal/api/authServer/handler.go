package authServer

import (
	"context"
	"errors"
	"strings"

	"authd/internal/service"
	"authd/pkg/database/sqlite"
	api "authd/pkg/grpc/auth"
	"authd/pkg/version"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Handler struct {
	api.UnimplementedAuthAPIServer
	service *service.Service
}

func NewHandler(authService *service.Service) *Handler {
	return &Handler{service: authService}
}

func (h *Handler) Login(ctx context.Context, req *api.LoginRequest) (*api.LoginResponse, error) {
	result, err := h.service.Login(ctx, service.LoginInput{
		Username:  req.GetUsername(),
		Password:  req.GetPassword(),
		ClientID:  req.GetClientId(),
		UserAgent: req.GetUserAgent(),
		ClientIP:  req.GetClientIp(),
	})
	if err != nil {
		return nil, toStatusError(err)
	}
	return &api.LoginResponse{
		UserId:       result.User.UserID,
		Username:     result.User.Username,
		DisplayName:  result.User.DisplayName,
		RefreshToken: result.RefreshToken,
		AccessToken:  result.AccessToken,
		ExpiresIn:    result.ExpiresIn,
	}, nil
}

func (h *Handler) VerifyToken(ctx context.Context, req *api.VerifyTokenRequest) (*api.VerifyTokenResponse, error) {
	claims, err := h.service.VerifyToken(ctx, req.GetAccessToken())
	if err != nil {
		return nil, toStatusError(err)
	}
	return &api.VerifyTokenResponse{
		Valid:     true,
		UserId:    claims.UserID,
		Username:  claims.Username,
		Roles:     append([]string(nil), claims.Roles...),
		ExpiresAt: timestamppb.New(claims.ExpiresAt.Time),
	}, nil
}

func (h *Handler) RefreshToken(ctx context.Context, req *api.RefreshTokenRequest) (*api.RefreshTokenResponse, error) {
	result, err := h.service.RefreshToken(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, toStatusError(err)
	}
	return &api.RefreshTokenResponse{
		RefreshToken: result.RefreshToken,
		AccessToken:  result.AccessToken,
		ExpiresIn:    result.ExpiresIn,
	}, nil
}

func (h *Handler) Logout(ctx context.Context, req *api.LogoutRequest) (*emptypb.Empty, error) {
	if err := h.service.Logout(ctx, req.GetRefreshToken()); err != nil {
		return nil, toStatusError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) GetProfile(ctx context.Context, req *api.GetProfileRequest) (*api.UserProfile, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	user, err := h.service.GetProfile(ctx, req.GetUserId(), claimsData)
	if err != nil {
		return nil, toStatusError(err)
	}
	return toProfile(user), nil
}

func (h *Handler) CreateUser(ctx context.Context, req *api.CreateUserRequest) (*api.UserProfile, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	user, err := h.service.CreateUser(ctx, service.CreateUserInput{
		Username:    req.GetUsername(),
		Password:    req.GetPassword(),
		DisplayName: req.GetDisplayName(),
		Roles:       req.GetRoles(),
		Enabled:     req.GetEnabled(),
	}, claimsData)
	if err != nil {
		return nil, toStatusError(err)
	}
	return toProfile(user), nil
}

func (h *Handler) UpdateUser(ctx context.Context, req *api.UpdateUserRequest) (*api.UserProfile, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	user, err := h.service.UpdateUser(ctx, service.UpdateUserInput{
		UserID:      req.GetUserId(),
		UpdateMask:  req.GetUpdateMask(),
		Password:    req.GetPassword(),
		DisplayName: req.GetDisplayName(),
		Roles:       req.GetRoles(),
		Enabled:     req.GetEnabled(),
	}, claimsData)
	if err != nil {
		return nil, toStatusError(err)
	}
	return toProfile(user), nil
}

func (h *Handler) DeleteUser(ctx context.Context, req *api.DeleteUserRequest) (*emptypb.Empty, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.service.DeleteUser(ctx, req.GetUserId(), claimsData); err != nil {
		return nil, toStatusError(err)
	}
	return &emptypb.Empty{}, nil
}

func (h *Handler) ListUsers(ctx context.Context, req *api.ListUsersRequest) (*api.ListUsersResponse, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	users, nextPageToken, err := h.service.ListUsers(ctx, req.GetPageSize(), req.GetPageToken(), req.GetKeyword(), req.GetEnabledOnly(), claimsData)
	if err != nil {
		return nil, toStatusError(err)
	}
	profiles := make([]*api.UserListItem, 0, len(users))
	for _, user := range users {
		profiles = append(profiles, toListItem(user))
	}
	return &api.ListUsersResponse{Users: profiles, NextPageToken: nextPageToken}, nil
}

func (h *Handler) CountUsers(ctx context.Context, req *api.CountUsersRequest) (*api.CountUsersResponse, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	count, err := h.service.CountUsers(ctx, req.GetKeyword(), req.GetEnabledOnly(), claimsData)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &api.CountUsersResponse{Count: count}, nil
}

func (h *Handler) ListRoles(ctx context.Context, req *emptypb.Empty) (*api.ListRolesResponse, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	roles, err := h.service.ListRoles(ctx, claimsData)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &api.ListRolesResponse{Roles: roles}, nil
}

func (h *Handler) GetAuthSettings(ctx context.Context, req *emptypb.Empty) (*api.AuthSettings, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := h.service.GetAuthSettings(ctx, claimsData)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &api.AuthSettings{
		ExtendRefreshTokenOnRefresh: settings,
	}, nil
}

func (h *Handler) UpdateAuthSettings(ctx context.Context, req *api.UpdateAuthSettingsRequest) (*api.AuthSettings, error) {
	claimsData, err := extractBearerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := h.service.UpdateAuthSettings(ctx, req.GetExtendRefreshTokenOnRefresh(), claimsData)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &api.AuthSettings{
		ExtendRefreshTokenOnRefresh: settings,
	}, nil
}

func (h *Handler) VersionGet(ctx context.Context, _ *emptypb.Empty) (*api.VersionInfo, error) {
	_ = ctx
	return &api.VersionInfo{
		Version: version.Version,
		Commit:  version.Commit,
		Date:    version.Date,
	}, nil
}

func extractBearerTokenFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization metadata")
	}

	raw := values[0]
	if len(raw) < 7 || !strings.EqualFold(raw[:7], "Bearer ") {
		return "", status.Error(codes.Unauthenticated, "invalid authorization scheme")
	}
	token := strings.TrimSpace(raw[7:])
	if token == "" {
		return "", status.Error(codes.Unauthenticated, "missing bearer token")
	}
	return token, nil
}

func toListItem(user *sqlite.User) *api.UserListItem {
	if user == nil {
		return nil
	}
	profile := &api.UserListItem{
		UserId:      user.UserID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Roles:       append([]string(nil), user.Roles...),
		Enabled:     user.Enabled,
	}
	if user.LastLoginAt != nil {
		profile.LastLoginAt = timestamppb.New(*user.LastLoginAt)
	}
	return profile
}

func toProfile(user *sqlite.User) *api.UserProfile {
	if user == nil {
		return nil
	}
	profile := &api.UserProfile{
		UserId:      user.UserID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Roles:       append([]string(nil), user.Roles...),
		Enabled:     user.Enabled,
		CreatedAt:   timestamppb.New(user.CreatedAt),
		UpdatedAt:   timestamppb.New(user.UpdatedAt),
	}
	if user.LastLoginAt != nil {
		profile.LastLoginAt = timestamppb.New(*user.LastLoginAt)
	}
	return profile
}

func toStatusError(err error) error {
	switch {
	case err == nil:
		return nil

	// === NotFound ===
	case errors.Is(err, sqlite.ErrUserNotFound):
		return status.Error(codes.NotFound, err.Error())

	// === AlreadyExists ===
	case errors.Is(err, sqlite.ErrUsernameExists):
		return status.Error(codes.AlreadyExists, err.Error())

	// === Unauthenticated（認證失敗）===
	case errors.Is(err, service.ErrInvalidCredentials),
		errors.Is(err, service.ErrInvalidToken):
		return status.Error(codes.Unauthenticated, err.Error())

	// === PermissionDenied（授權失敗）===
	case errors.Is(err, service.ErrPermissionDenied),
		errors.Is(err, service.ErrForbiddenFieldUpdate):
		return status.Error(codes.PermissionDenied, err.Error())

	// === FailedPrecondition（禁止對自己執行的操作）===
	case errors.Is(err, service.ErrForbiddenSelfAction):
		return status.Error(codes.FailedPrecondition, err.Error())

	// === ResourceExhausted ===
	case errors.Is(err, service.ErrUserLimitExceeded):
		return status.Error(codes.ResourceExhausted, err.Error())

	// === InvalidArgument ===
	case errors.Is(err, service.ErrInvalidArgument),
		errors.Is(err, service.ErrInvalidUpdateMask),
		errors.Is(err, service.ErrInvalidPassword):
		return status.Error(codes.InvalidArgument, err.Error())

	// === Internal（fallback）===
	default:
		log.Error().Err(err).Msg("internal error")
		return status.Error(codes.Internal, "internal server error")
	}
}
