// Minimal test-only resource actor probes Temporal messaging, not the proposed scheduler policy.
package admissionprobe

import (
	"fmt"

	"go.temporal.io/sdk/workflow"
)

const protocolQueue = "ff038-disposable-protocol"

// protocolReceipt proves whether one named mutation consumed the toy allowance.
type protocolReceipt struct {
	ID      string
	Granted bool
	Debits  int
}

// protocolState carries receipts across replay and Continue-As-New.
type protocolState struct {
	Debits   int
	Receipts map[string]protocolReceipt
}

// protocolWorkflow exposes a signal/receipt path and an Update for compatibility comparison.
func protocolWorkflow(ctx workflow.Context, state protocolState) error {
	if state.Receipts == nil {
		state.Receipts = make(map[string]protocolReceipt)
	}
	apply := func(id string) protocolReceipt {
		if receipt, exists := state.Receipts[id]; exists {
			return receipt
		}
		receipt := protocolReceipt{ID: id, Granted: state.Debits < 2}
		if receipt.Granted {
			state.Debits++
		}
		receipt.Debits = state.Debits
		state.Receipts[id] = receipt
		return receipt
	}
	if err := workflow.SetQueryHandler(ctx, "receipt", func(id string) (protocolReceipt, error) {
		receipt, exists := state.Receipts[id]
		if !exists {
			return protocolReceipt{}, fmt.Errorf("receipt not processed")
		}
		return receipt, nil
	}); err != nil {
		return err
	}
	if err := workflow.SetUpdateHandler(ctx, "reserve", func(_ workflow.Context, id string) (protocolReceipt, error) {
		return apply(id), nil
	}); err != nil {
		return err
	}
	requests := workflow.GetSignalChannel(ctx, "reserve")
	rotate := workflow.GetSignalChannel(ctx, "rotate")
	stop := workflow.GetSignalChannel(ctx, "stop")
	for {
		rotation, stopped := false, false
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(requests, func(ch workflow.ReceiveChannel, _ bool) {
			var id string
			ch.Receive(ctx, &id)
			apply(id)
		})
		selector.AddReceive(rotate, func(ch workflow.ReceiveChannel, _ bool) {
			ch.Receive(ctx, nil)
			rotation = true
		})
		selector.AddReceive(stop, func(ch workflow.ReceiveChannel, _ bool) {
			ch.Receive(ctx, nil)
			stopped = true
		})
		selector.Select(ctx)
		if rotation || stopped {
			var id string
			for requests.ReceiveAsync(&id) {
				apply(id)
			}
			if stopped {
				return nil
			}
			return workflow.NewContinueAsNewError(ctx, protocolWorkflow, state)
		}
	}
}
