// provider_integrity_test.go pins opt-in enforcement and binary isolation.
package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProviderIntegrityMode verifies the production-safe default and rejects
// invalid worker modes without coupling API startup to worker-only settings.
func TestProviderIntegrityMode(t *testing.T) {
	setValidWorkerEnvironment(t)
	t.Setenv("PROVIDER_INTEGRITY_MODE", "")
	cfg, err := LoadFor(BinaryWorker)
	require.NoError(t, err)
	require.Equal(t, "shadow", cfg.ProviderIntegrity.Mode)
	t.Setenv("PROVIDER_INTEGRITY_MODE", "enforce")
	cfg, err = LoadFor(BinaryWorker)
	require.NoError(t, err)
	require.Equal(t, "enforce", cfg.ProviderIntegrity.Mode)
	t.Setenv("PROVIDER_INTEGRITY_MODE", "enfore")
	_, err = LoadFor(BinaryWorker)
	require.ErrorContains(t, err, "PROVIDER_INTEGRITY_MODE")
	setValidAPIEnvironment(t)
	_, err = LoadFor(BinaryAPI)
	require.NoError(t, err)
}
