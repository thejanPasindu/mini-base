// Package ports holds the interfaces and value types that modules share.
// A module that needs something from another module depends on a port here,
// never on the other module's packages.
package ports

import "context"

// Role is the caller's privilege level. It decides which Postgres role a
// database request runs as.
type Role string

// Roles.
const (
	// RoleAnon is a public client holding only the anon API key.
	RoleAnon Role = "anon"
	// RoleAuthenticated is a signed-in user (anon key + valid access token).
	RoleAuthenticated Role = "authenticated"
	// RoleService is a trusted server holding the service API key. It
	// bypasses row-level security.
	RoleService Role = "service"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	Role      Role
	UserID    string         // set for RoleAuthenticated
	SessionID string         // set for RoleAuthenticated
	Claims    map[string]any // exposed to SQL as request.jwt.claims
}

// TokenVerifier verifies access tokens. The auth module provides it under
// TokenVerifierService; other modules use it through the kernel.
type TokenVerifier interface {
	VerifyAccessToken(ctx context.Context, token string) (Principal, error)
}

// TokenVerifierService is the kernel.Services name of the TokenVerifier.
const TokenVerifierService = "auth.token_verifier"
