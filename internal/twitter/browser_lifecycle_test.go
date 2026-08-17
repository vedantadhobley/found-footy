// browser_lifecycle_test.go — unit coverage for the one-shot critical-child
// signal used when Firefox or its persistent context exits.
package twitter

import "testing"

func TestBrowserDoneClosesOnce(t *testing.T) {
	browser := &Browser{done: make(chan struct{})}
	browser.markDone()
	browser.markDone()

	select {
	case <-browser.Done():
	default:
		t.Fatal("Done remained open after browser exit")
	}
}
