// main_test.go — process-boundary tests for the Twitter HTTP server and its
// critical Firefox child without starting either real process.
package main

import (
	"errors"
	"net/http"
	"testing"
)

func TestWaitForBrowserOrServer_BrowserExitIsFatal(t *testing.T) {
	browserDone := make(chan struct{})
	serverDone := make(chan error)
	marked := 0
	close(browserDone)

	err := waitForBrowserOrServer(browserDone, serverDone, func() { marked++ })
	if !errors.Is(err, errBrowserExited) {
		t.Fatalf("error = %v, want browser-exited sentinel", err)
	}
	if marked != 1 {
		t.Fatalf("failure transition calls = %d, want 1", marked)
	}
}

func TestWaitForBrowserOrServer_ServerResultWins(t *testing.T) {
	browserDone := make(chan struct{})
	serverDone := make(chan error, 1)
	serverDone <- http.ErrServerClosed
	if err := waitForBrowserOrServer(browserDone, serverDone, nil); err != nil {
		t.Fatalf("graceful server close = %v, want nil", err)
	}
}
