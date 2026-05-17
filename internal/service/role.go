package service

// Role constants
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// Permission constants
const (
	PermUserCreate     = "user.create"
	PermUserRead       = "user.read"   // 讀他人
	PermUserUpdate     = "user.update" // 改他人
	PermUserDelete     = "user.delete" // 刪他人
	PermUserList       = "user.list"
	PermSettingsRead   = "settings.read"
	PermSettingsUpdate = "settings.update"
)

type PermissionSet map[string]struct{}

var rolePermissions = map[string]PermissionSet{
	RoleAdmin: {
		PermUserCreate:     {},
		PermUserRead:       {},
		PermUserUpdate:     {},
		PermUserDelete:     {},
		PermUserList:       {},
		PermSettingsRead:   {},
		PermSettingsUpdate: {},
	},
	RoleUser: {
		PermUserList: {},
	},
}

func (p PermissionSet) Has(perm string) bool {
	_, ok := p[perm]
	return ok
}

func ResolvePermissions(roles []string) PermissionSet {
	result := PermissionSet{}
	for _, role := range roles {
		for perm := range rolePermissions[role] {
			result[perm] = struct{}{}
		}
	}
	return result
}
