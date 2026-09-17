// Publish the single approved repair invalidation through the normal producer.
//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/event"
	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// main restricts this operation to a connection check or the approved event.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run checks the normal producer path; apply emits exactly one invalidation.
func run() error {
	if len(os.Args) != 2 || (os.Args[1] != "--check" && os.Args[1] != "--publish-approved") {
		return fmt.Errorf("explicit --check or --publish-approved required")
	}
	if os.Getenv("NATS_URL") != "nats://nats:4222" {
		return fmt.Errorf("connection configuration changed")
	}
	conn, err := nats.New(context.Background(), config.NATSConfig{
		URL: os.Getenv("NATS_URL"), ClientName: "found-footy-ff081-maitland-repair",
		ConnectTimeout: 5 * time.Second, ReconnectWait: time.Second, MaxReconnects: 0,
	}, nats.RegisterMetrics(metrics.New(), &logging.TestEmitter{}))
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.FlushTimeout(5 * time.Second); err != nil {
		return err
	}
	if os.Args[1] == "--check" {
		fmt.Println("NATS_CONNECTION_CHECK_PASS; nothing published")
		return nil
	}
	const subject = "found-footy.prod.event.update"
	sub, err := conn.SubscribeSync(subject)
	if err != nil {
		return err
	}
	if err := conn.FlushTimeout(5 * time.Second); err != nil {
		return err
	}
	pub, err := event.NewPublisher(conn, "prod")
	if err != nil {
		return err
	}
	if err := pub.PublishEventUpdate(uuid.MustParse("01dedc54-da16-4e3c-9539-ac43c81ffd77"), 1557390); err != nil {
		return err
	}
	if err := conn.FlushTimeout(5 * time.Second); err != nil {
		return err
	}
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		return err
	}
	var envelope event.Envelope
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		return err
	}
	if envelope.Subject != subject || envelope.Source != "found-footy-prod" || envelope.Version != 1 {
		return fmt.Errorf("unexpected broker envelope")
	}
	fmt.Printf("NATS_REPAIR_PUBLISHED %s\n", msg.Data)
	return nil
}
