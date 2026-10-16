// Real-server protocol research runs only inside this directory's private disposable Compose stack.
package admissionprobe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"
)

// dialProbe refuses arbitrary targets; Compose isolates temporal:7233 from application networks.
func dialProbe(t *testing.T) client.Client {
	t.Helper()
	if testing.Short() || os.Getenv("FF038_DISPOSABLE_SERVER") != "1" {
		t.Skip("requires the network-isolated FF-038 research Compose stack")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, client.Options{HostPort: "temporal:7233", Namespace: "ff038-research"})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	_, err = c.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{
		Namespace: "ff038-research", WorkflowExecutionRetentionPeriod: durationpb.New(time.Hour),
	})
	var exists *serviceerror.NamespaceAlreadyExists
	if !errors.As(err, &exists) {
		require.NoError(t, err)
	}
	return c
}

// startProbeWorker registers only the minimal research workflow.
func startProbeWorker(t *testing.T, c client.Client) worker.Worker {
	t.Helper()
	w := worker.New(c, protocolQueue, worker.Options{WorkerStopTimeout: time.Second})
	w.RegisterWorkflow(protocolWorkflow)
	require.NoError(t, w.Start())
	return w
}

// readReceipt retrieves a named mutation result without authorizing work in a Query.
func readReceipt(ctx context.Context, c client.Client, workflowID, id string) (protocolReceipt, error) {
	encoded, err := c.QueryWorkflow(ctx, workflowID, "", "receipt", id)
	if err != nil {
		return protocolReceipt{}, err
	}
	var receipt protocolReceipt
	err = encoded.Get(&receipt)
	return receipt, err
}

// TestProtocolSignalReceiptRecovery tests mutation identity, live replay, history rotation and local latency.
func TestProtocolSignalReceiptRecovery(t *testing.T) {
	c := dialProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	info, err := c.WorkflowService().GetSystemInfo(ctx, &workflowservice.GetSystemInfoRequest{})
	require.NoError(t, err)
	t.Logf("disposable_server_version=%s", info.ServerVersion)
	w := startProbeWorker(t, c)
	running := true
	t.Cleanup(func() {
		if running {
			w.Stop()
		}
	})
	workflowID := "ff038-protocol-" + uuid.NewString()
	run, err := c.SignalWithStartWorkflow(ctx, workflowID, "reserve", "request-a",
		client.StartWorkflowOptions{ID: workflowID, TaskQueue: protocolQueue, WorkflowExecutionTimeout: 90 * time.Second},
		protocolWorkflow, protocolState{})
	require.NoError(t, err)
	receipt, err := readReceipt(ctx, c, workflowID, "request-a")
	require.NoError(t, err, "signal acknowledgment must be followed by a processed receipt")
	require.Equal(t, protocolReceipt{ID: "request-a", Granted: true, Debits: 1}, receipt)

	// Lose the caller before reading its result, then repeat the same mutation.
	require.NoError(t, c.SignalWorkflow(ctx, workflowID, "", "reserve", "request-b"))
	restartedClient := dialProbe(t)
	require.NoError(t, restartedClient.SignalWorkflow(ctx, workflowID, "", "reserve", "request-b"))
	receipt, err = readReceipt(ctx, restartedClient, workflowID, "request-b")
	require.NoError(t, err)
	require.Equal(t, protocolReceipt{ID: "request-b", Granted: true, Debits: 2}, receipt)

	latencies := make([]time.Duration, 0, 100)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("denied-%d", i)
		started := time.Now()
		require.NoError(t, c.SignalWorkflow(ctx, workflowID, "", "reserve", id))
		receipt, err = readReceipt(ctx, c, workflowID, id)
		require.NoError(t, err)
		require.Equal(t, protocolReceipt{ID: id, Granted: false, Debits: 2}, receipt)
		latencies = append(latencies, time.Since(started))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("signal_plus_receipt_calls=100 median=%s p95=%s max=%s", latencies[50], latencies[94], latencies[99])

	w.Stop()
	running = false
	w = startProbeWorker(t, c)
	running = true
	receipt, err = readReceipt(ctx, c, workflowID, "request-b")
	require.NoError(t, err)
	require.Equal(t, 2, receipt.Debits, "worker replacement must preserve receipts")
	require.NoError(t, c.SignalWorkflow(ctx, workflowID, "", "reserve", "request-c"))
	receipt, err = readReceipt(ctx, c, workflowID, "request-c")
	require.NoError(t, err)
	require.False(t, receipt.Granted, "a fresh request cannot reuse exhausted allowance")

	require.NoError(t, c.SignalWorkflow(ctx, workflowID, "", "rotate", nil))
	require.Eventually(t, func() bool {
		description, err := c.DescribeWorkflowExecution(ctx, workflowID, "")
		return err == nil && description.WorkflowExecutionInfo.Execution.RunId != run.GetRunID()
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, c.SignalWorkflow(ctx, workflowID, "", "reserve", "request-b"))
	receipt, err = readReceipt(ctx, c, workflowID, "request-b")
	require.NoError(t, err)
	require.Equal(t, protocolReceipt{ID: "request-b", Granted: true, Debits: 2}, receipt)
	require.NoError(t, c.SignalWorkflow(ctx, workflowID, "", "stop", nil))
	require.NoError(t, run.Get(ctx, nil))
	t.Log("signal_receipt_retry_worker_restart_continue_as_new=passed")
}

// TestProtocolUpdateAvailability records default-server support instead of assuming a modern SDK enables it.
func TestProtocolUpdateAvailability(t *testing.T) {
	c := dialProbe(t)
	w := startProbeWorker(t, c)
	defer w.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	workflowID := "ff038-update-" + uuid.NewString()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: workflowID,
		TaskQueue: protocolQueue, WorkflowExecutionTimeout: 30 * time.Second}, protocolWorkflow, protocolState{})
	require.NoError(t, err)
	handle, updateErr := c.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID: workflowID, UpdateID: "request-a", UpdateName: "reserve",
		WaitForStage: client.WorkflowUpdateStageCompleted, Args: []interface{}{"request-a"},
	})
	if updateErr != nil {
		var unavailable *serviceerror.PermissionDenied
		var unimplemented *serviceerror.Unimplemented
		if errors.As(updateErr, &unavailable) || errors.As(updateErr, &unimplemented) {
			t.Logf("update_default_server=unavailable type=%T reason=%s", updateErr, updateErr)
		} else {
			t.Fatal(fmt.Errorf("unexpected Update compatibility result: %w", updateErr))
		}
	} else {
		var receipt protocolReceipt
		require.NoError(t, handle.Get(ctx, &receipt))
		require.Equal(t, protocolReceipt{ID: "request-a", Granted: true, Debits: 1}, receipt)
		t.Log("update_default_server=supported")
	}
	require.NoError(t, c.SignalWorkflow(ctx, workflowID, "", "stop", nil))
	require.NoError(t, run.Get(ctx, nil))
}
