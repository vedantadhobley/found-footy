// Internal denial-reader tests enforce the byte bound independently of HTTP buffering.
package syndication

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// denialCountingReader measures consumption from an oversized upstream body.
type denialCountingReader struct {
	io.Reader
	read int
}

// Read counts only bytes consumed by classification, not transport prefetch.
func (r *denialCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

// TestClassifyCDNDenial_ReadBound requires one overflow byte and no further read.
func TestClassifyCDNDenial_ReadBound(t *testing.T) {
	body := &denialCountingReader{Reader: strings.NewReader("Geo Forbidden" + strings.Repeat(" ", 4096))}
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/plain"}},
		Body:   io.NopCloser(body),
	}
	if got := classifyCDNDenial(resp); got != ErrCDNForbidden {
		t.Fatalf("oversized denial = %v, want unknown/retryable", got)
	}
	if body.read != cdnDenialMaxBytes+1 {
		t.Fatalf("read %d bytes, want overflow bound %d", body.read, cdnDenialMaxBytes+1)
	}
}
