package authn

import "context"

type Principal struct {
	ID          string
	Username    string
	Email       string
	SuperAdmin  bool
	Status      string
	AvatarURL   string
	TrustLevel  int
	Locale      string
	TOTPEnabled bool
	Permissions map[string]struct{}
}

func (p *Principal) Can(permission string) bool {
	if p == nil {
		return false
	}
	if p.SuperAdmin {
		return true
	}
	_, ok := p.Permissions[permission]
	return ok
}

type ctxKey struct{}

// Loader resolves a principal from a user ID. Implemented by the auth module adapter.
type Loader interface {
	Load(ctx context.Context, id string) (*Principal, error)
}

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
