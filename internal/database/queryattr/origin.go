package queryattr

import "context"

// Origin is the bounded ownership class of an operation.
type Origin uint8

const (
	BackgroundOrigin Origin = iota
	RequestOrigin
	InternalOrigin
)

// Component names are fixed; internal work never belongs to a request.
type Component uint8

const (
	TelemetryPersistence Component = iota
	ConnectionSetup
)

type ownership struct {
	context.Context
	origin    Origin
	component Component
	category  string
	scope     Scope
}
type ownershipKey struct{}

func (o *ownership) Value(key any) any {
	if _, ok := key.(ownershipKey); ok {
		return o
	}
	return o.Context.Value(key)
}

func ownershipFrom(ctx context.Context) ownership {
	if own, ok := ctx.Value(ownershipKey{}).(*ownership); ok {
		return *own
	}
	return ownership{}
}

// Internal shadows inherited ownership with a fixed internal component.
func Internal(ctx context.Context, c Component) context.Context {
	return &ownership{Context: ctx, origin: InternalOrigin, component: c}
}

// WithOwnership attaches request or background ownership. A new owner replaces
// the inherited owner, including its request scope.
func WithOwnership(ctx context.Context, o Origin, scope Scope) context.Context {
	return &ownership{Context: ctx, origin: o, scope: scope}
}

// Background transfers ownership to a bounded workload category.
func Background(ctx context.Context, category string) context.Context {
	return &ownership{Context: ctx, origin: BackgroundOrigin, category: category}
}
