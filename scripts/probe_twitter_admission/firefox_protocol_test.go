// Real-Firefox local fixtures probe paused routing and cursor preservation, never X semantics.
package admissionprobe

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"
)

// TestFirefoxPausedRoute uses the image-owned driver with no public network or credentials.
func TestFirefoxPausedRoute(t *testing.T) {
	if testing.Short() || os.Getenv("FF038_DISPOSABLE_BROWSER") != "1" {
		t.Skip("requires a disposable, network-isolated pinned Twitter image")
	}
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/SearchTimeline" {
			sent.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"page":%s}`, r.URL.Query().Get("page"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>Retained fixture</title><script>
window.cursor = 0;
window.records = [];
window.nextPage = function() {
  window.pending = fetch('/SearchTimeline?page=' + window.cursor)
    .then(response => response.json())
    .then(data => { window.records.push(data.page); window.cursor++; })
    .catch(() => {});
  return true;
};
</script>`))
	}))
	defer srv.Close()
	pw, err := playwright.Run()
	require.NoError(t, err)
	defer func() { require.NoError(t, pw.Stop()) }()
	browser, err := pw.Firefox.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(true)})
	require.NoError(t, err)
	defer func() { require.NoError(t, browser.Close()) }()
	bctx, err := browser.NewContext()
	require.NoError(t, err)
	pending := make(chan playwright.Route, 4)
	require.NoError(t, bctx.Route(regexp.MustCompile(`/SearchTimeline\?`), func(route playwright.Route) {
		pending <- route // the route stays local until the later continuation call
	}))
	page, err := bctx.NewPage()
	require.NoError(t, err)
	page.SetDefaultTimeout(3000)
	_, err = page.Goto(srv.URL)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err = page.Evaluate(`window.nextPage()`)
		require.NoError(t, err)
		var route playwright.Route
		select {
		case route = <-pending:
		case <-time.After(3 * time.Second):
			t.Fatal("local request never reached the route hook")
		}
		require.Equal(t, int32(i), sent.Load(), "paused request reached upstream without permission")
		cursor, err := page.Evaluate(`window.cursor`)
		require.NoError(t, err, "pending routing must not block other Go driver calls")
		require.EqualValues(t, i, cursor)
		require.NoError(t, route.Continue())
		handle, err := page.WaitForFunction(fmt.Sprintf("window.cursor === %d", i+1), nil)
		require.NoError(t, err)
		require.NoError(t, handle.Dispose())
	}
	records, err := page.Evaluate(`window.records`)
	require.NoError(t, err)
	require.Equal(t, []interface{}{0, 1, 2}, records)
	_, err = page.Evaluate(`window.nextPage()`)
	require.NoError(t, err)
	select {
	case route := <-pending:
		require.NoError(t, route.Abort())
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation fixture did not pause")
	}
	require.NoError(t, page.Close())
	require.Equal(t, int32(3), sent.Load(), "canceled request must not reach upstream")
	t.Log("paused_go_route_driver_responsive_cursor_preserved_cancellation=passed")
}
