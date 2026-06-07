package identity

import "context"

type Actor struct {
	UserID      string
	Username    string
	DisplayName string
	IsAdmin     bool
}

type contextKey string

const actorContextKey contextKey = "identity.actor"

func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorContextKey, actor)
}

func ActorFromContext(ctx context.Context) *Actor {
	if ctx == nil {
		return nil
	}
	value, ok := ctx.Value(actorContextKey).(Actor)
	if !ok {
		return nil
	}
	return &value
}
