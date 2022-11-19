package api

import "context"

type depotTokenKey struct{}

func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, depotTokenKey{}, token)
}

func GetContextToken(ctx context.Context) string {
	return ctx.Value(depotTokenKey{}).(string)
}
