package api

import "context"

const depotClientKey = "depot.client"

func WithClient(ctx context.Context, client *Depot) context.Context {
	return context.WithValue(ctx, depotClientKey, client)
}

func GetContextClient(ctx context.Context) *Depot {
	return ctx.Value(depotClientKey).(*Depot)
}
