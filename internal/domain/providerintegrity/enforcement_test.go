// enforcement_test.go proves recommendation composition is rollout-independent.
package providerintegrity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDecideMutation covers all scope combinations in both rollout modes.
func TestDecideMutation(t *testing.T) {
	policies := []MutationPolicy{PolicyTrusted, PolicyPositiveOnly, PolicyRejected}
	for _, mode := range []EnforcementMode{EnforcementShadow, EnforcementEnforce} {
		for _, provider := range policies {
			for _, fixture := range policies {
				for _, local := range policies {
					d, applied := DecideMutation(mode, provider, fixture, local)
					require.Equal(t, RestrictPolicies(provider, fixture, local), d.RecommendedPolicy)
					require.Equal(t, mode, d.Mode)
					require.Equal(t, provider, d.ProviderPolicy)
					require.Equal(t, fixture, d.FixturePolicy)
					require.Equal(t, local, d.LocalPolicy)
					if mode == EnforcementShadow {
						require.Equal(t, PolicyTrusted, applied)
					} else {
						require.Equal(t, d.RecommendedPolicy, applied)
					}
				}
			}
		}
	}
}
