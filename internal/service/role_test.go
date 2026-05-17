package service

import (
	"testing"
)

func TestPermissionSet_Has(t *testing.T) {
	pset := PermissionSet{
		PermUserCreate: {},
		PermUserRead:   {},
	}

	if !pset.Has(PermUserCreate) {
		t.Errorf("expected to have permission %s", PermUserCreate)
	}

	if !pset.Has(PermUserRead) {
		t.Errorf("expected to have permission %s", PermUserRead)
	}

	if pset.Has(PermUserDelete) {
		t.Errorf("expected NOT to have permission %s", PermUserDelete)
	}

	if pset.Has("some.unknown.permission") {
		t.Errorf("expected NOT to have permission some.unknown.permission")
	}
}

func TestResolvePermissions(t *testing.T) {
	t.Run("Empty Roles", func(t *testing.T) {
		pset := ResolvePermissions(nil)
		if len(pset) != 0 {
			t.Errorf("expected empty permissions for empty roles, got %d", len(pset))
		}
	})

	t.Run("Unknown Role", func(t *testing.T) {
		pset := ResolvePermissions([]string{"unknown_role"})
		if len(pset) != 0 {
			t.Errorf("expected empty permissions for unknown role, got %d", len(pset))
		}
	})

	t.Run("Single Role User", func(t *testing.T) {
		pset := ResolvePermissions([]string{RoleUser})
		if len(pset) != len(rolePermissions[RoleUser]) {
			t.Errorf("expected %d permissions for %s, got %d", len(rolePermissions[RoleUser]), RoleUser, len(pset))
		}
		if !pset.Has(PermUserList) {
			t.Errorf("expected %s to have permission %s", RoleUser, PermUserList)
		}
	})

	t.Run("Single Role Admin", func(t *testing.T) {
		pset := ResolvePermissions([]string{RoleAdmin})
		if len(pset) != len(rolePermissions[RoleAdmin]) {
			t.Errorf("expected %d permissions for %s, got %d", len(rolePermissions[RoleAdmin]), RoleAdmin, len(pset))
		}
		if !pset.Has(PermUserCreate) || !pset.Has(PermUserDelete) || !pset.Has(PermSettingsUpdate) {
			t.Errorf("expected %s to have required permissions", RoleAdmin)
		}
	})

	t.Run("Multiple Roles", func(t *testing.T) {
		// Even if roles overlap, permission set should deduplicate
		pset := ResolvePermissions([]string{RoleUser, RoleAdmin, "unknown_role"})
		expectedLen := len(rolePermissions[RoleAdmin]) // since Admin contains all User perms in our definition
		if len(pset) != expectedLen {
			t.Errorf("expected %d permissions for combined roles, got %d", expectedLen, len(pset))
		}
	})
}
