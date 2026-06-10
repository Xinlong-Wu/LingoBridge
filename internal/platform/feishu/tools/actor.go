package tools

import (
	"context"
	"strings"
)

type actorContextKey struct{}

// Actor describes the Feishu user who triggered a tool call.
type Actor struct {
	OpenID string
	UserID string
	Name   string
	Email  string
}

// WithActor attaches sanitized Feishu sender identity to a tool execution context.
func WithActor(ctx context.Context, actor Actor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	actor = normalizeActor(actor)
	if actor.OpenID == "" && actor.UserID == "" && actor.Name == "" && actor.Email == "" {
		return ctx
	}
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// ActorFromContext returns the Feishu sender attached by WithActor.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	if ctx == nil {
		return Actor{}, false
	}
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	if !ok {
		return Actor{}, false
	}
	actor = normalizeActor(actor)
	return actor, actor.OpenID != "" || actor.UserID != "" || actor.Name != "" || actor.Email != ""
}

func normalizeActor(actor Actor) Actor {
	actor.OpenID = strings.TrimSpace(actor.OpenID)
	actor.UserID = strings.TrimSpace(actor.UserID)
	actor.Name = strings.TrimSpace(actor.Name)
	actor.Email = strings.TrimSpace(actor.Email)
	return actor
}
