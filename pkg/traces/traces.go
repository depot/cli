package traces

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/docker/buildx/util/tracing"
)

const DEPOT_BUILD_ID = "depot.build.id"

func TraceCommand(ctx context.Context, name, buildID, token string) (context.Context, func(error), error) {
	vars := map[string]string{}

	vars["OTEL_RESOURCE_ATTRIBUTES"] = encode(DEPOT_BUILD_ID, buildID)
	vars["OTEL_EXPORTER_OTLP_HEADERS"] = encode("Authorization", fmt.Sprintf("Bearer %s", token))

	vars["OTEL_TRACES_EXPORTER"] = "otlp"
	vars["OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"] = "grpc"
	vars["OTEL_EXPORTER_OTLP_COMPRESSION"] = "gzip"

	// Restrict to not send too much data. Perhaps more?
	vars["OTEL_EVENT_ATTRIBUTE_COUNT_LIMIT"] = "8"
	vars["OTEL_SPAN_LINK_COUNT_LIMIT"] = "8"
	vars["OTEL_LINK_ATTRIBUTE_COUNT_LIMIT"] = "8"
	vars["OTEL_SPAN_EVENT_COUNT_LIMIT"] = "8"

	// TODO: remove this once we have a proper collector
	vars["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] = "http://localhost:6673"
	vars["OTEL_EXPORTER_OTLP_INSECURE"] = "true"

	for k, v := range vars {
		if err := os.Setenv(k, v); err != nil {
			return context.Background(), nil, err
		}
	}

	return tracing.TraceCurrentCommand(ctx, name)
}

func encode(key, value string) string {
	return fmt.Sprintf("%s=%s", key, url.QueryEscape(value))
}
