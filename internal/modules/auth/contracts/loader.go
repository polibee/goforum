package contracts

import (
	"context"

	"goforum/internal/platform/authn"
)

type loader struct{ auth Authenticator }

// AsLoader adapts auth authentication to the platform middleware loader.
func AsLoader(auth Authenticator) authn.Loader { return loader{auth: auth} }

func (l loader) Load(ctx context.Context, id string) (*authn.Principal, error) {
	u, err := l.auth.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	perms := map[string]struct{}{}
	if u.Status == "active" {
		if resolved, err := l.auth.PermissionsOf(ctx, id); err == nil {
			perms = resolved
		}
	}
	return &authn.Principal{
		ID:          u.ID,
		Username:    u.Username,
		Email:       u.Email,
		SuperAdmin:  u.SuperAdmin,
		Status:      u.Status,
		AvatarURL:   u.AvatarURL,
		TrustLevel:  u.TrustLevel,
		Locale:      u.Locale,
		TOTPEnabled: u.TOTPEnabled,
		Permissions: perms,
	}, nil
}
