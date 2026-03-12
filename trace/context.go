package trace

import "context"

type interactionIDContextKey struct{}

func ContextWithInteractionID(ctx context.Context, interactionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, interactionIDContextKey{}, interactionID)
}

func InteractionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(interactionIDContextKey{}).(string)
	return value
}
