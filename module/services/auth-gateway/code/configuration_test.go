package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceEnvResolvesConfigurationSecretAndFallback(t *testing.T) {
	const key = "CODEFLY_GATEWAY_TOKEN"
	configurationKey := "CODEFLY__WORKSPACE_CONFIGURATION__INTERNAL-AUTH__" + key
	secretKey := "CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL-AUTH__" + key
	legacySecretKey := "CODEFLY__WORKSPACE_SECRET_CONFIGURATION__INTERNAL_AUTH__" + key

	t.Setenv(key, "plain")
	t.Setenv(configurationKey, "")
	t.Setenv(secretKey, "")
	t.Setenv(legacySecretKey, "legacy-secret")
	require.Equal(t, "legacy-secret", workspaceEnv("internal-auth", key))

	t.Setenv(legacySecretKey, "")
	require.Equal(t, "plain", workspaceEnv("internal-auth", key))

	t.Setenv(secretKey, "secret")
	require.Equal(t, "secret", workspaceEnv("internal-auth", key))

	t.Setenv(configurationKey, "configuration")
	require.Equal(t, "configuration", workspaceEnv("internal-auth", key))
}

func TestObservabilityConfigurationUsesWorkspaceState(t *testing.T) {
	keys := []string{"OBSERVABILITY_STATE", "OBSERVABILITY_DISABLED_REASON", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL", "OBSERVABILITY_COLLECTOR_TIER"}
	for _, key := range keys {
		t.Setenv(key, "")
		t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__"+key, "")
		t.Setenv("CODEFLY__WORKSPACE_SECRET_CONFIGURATION__OBSERVABILITY__"+key, "")
	}
	_, err := configuredObservability()
	require.Error(t, err)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://stale.example:4317")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OBSERVABILITY_STATE", "disabled")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OBSERVABILITY_DISABLED_REASON", "no backend")
	c, err := configuredObservability()
	require.NoError(t, err)
	require.Equal(t, "disabled", c.State)
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OBSERVABILITY_STATE", "enabled")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OTEL_EXPORTER_OTLP_ENDPOINT", "http://cell-collector.example:14317")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("CODEFLY__WORKSPACE_CONFIGURATION__OBSERVABILITY__OBSERVABILITY_COLLECTOR_TIER", "node-agent")
	c, err = configuredObservability()
	require.NoError(t, err)
	require.Equal(t, "http://cell-collector.example:14317", c.Endpoint)
}
