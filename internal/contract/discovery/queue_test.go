// Queue-name tests prevent deployment-specific routing from entering workflows.
package discovery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVisionTaskQueue derives one lane from any already-selected base queue.
func TestVisionTaskQueue(t *testing.T) {
	for _, base := range []string{"found-footy", "found-footy-dev", "custom-queue"} {
		require.Equal(t, base+"-vision", VisionTaskQueue(base))
	}
}
