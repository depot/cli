package builder

import "context"

func WithBuilders(ctx context.Context, builders []*Builder) context.Context {
	return context.WithValue(ctx, "depot.builders", builders)
}

func GetContextBuilders(ctx context.Context) []*Builder {
	builders, ok := ctx.Value("depot.builders").([]*Builder)
	if !ok {
		return nil
	}
	return builders
}
