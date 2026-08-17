// Package nats is the workspace NATS bus adapter. Per decisions.md
// 2026-07-01, NATS is the metadata plane — SSE fan-out, webhook
// delivery, cross-project events. Video bytes never traverse it.
//
// Conn adds publish instrumentation and disconnect/reconnect telemetry to the
// underlying client.
package nats

import (
	"context"
	"fmt"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Conn wraps *nats.Conn (embedded, so JetStream(), Request(),
// RequestWithContext(), Flush(), etc. are directly callable) with
// lifecycle log emissions + per-publish metric instrumentation. Same
// pattern as pg.Pool.
type Conn struct {
	*natsgo.Conn
	ins *Instruments
}

// New opens a NATS connection to cfg.URL using the account credentials
// at cfg.CredsFile (if set). Wires disconnect/reconnect/close callbacks
// so lifecycle events land in both logs + metrics. Bounded by
// cfg.ConnectTimeout — never hangs binary startup on a dead server.
//
// The caller is responsible for calling Close when done. Register with
// bootstrap.Deps.RegisterCloser("nats", ...) so LIFO shutdown ordering
// runs it before its downstream deps.
func New(ctx context.Context, cfg config.NATSConfig, ins *Instruments) (*Conn, error) {
	if ins == nil {
		return nil, fmt.Errorf("nats.New: Instruments is required (call RegisterMetrics first)")
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("nats.New: NATS_URL not set")
	}

	c := &Conn{ins: ins}

	opts := []natsgo.Option{
		natsgo.Name(cfg.ClientName),
		natsgo.Timeout(cfg.ConnectTimeout),
		natsgo.ReconnectWait(cfg.ReconnectWait),
		natsgo.MaxReconnects(cfg.MaxReconnects),
		natsgo.DisconnectErrHandler(func(_ *natsgo.Conn, err error) {
			c.ins.connectionState.Set(0)
			c.ins.emitEvent(context.Background(), logging.LevelWarn,
				vocabulary.ActionNATSDisconnected,
				"nats disconnected",
				logging.Err(err),
			)
		}),
		natsgo.ReconnectHandler(func(nc *natsgo.Conn) {
			c.ins.connectionState.Set(1)
			c.ins.reconnects.Inc()
			c.ins.emitEvent(context.Background(), logging.LevelInfo,
				vocabulary.ActionNATSReconnected,
				"nats reconnected",
				logging.String("connected_url", nc.ConnectedUrl()),
			)
		}),
		natsgo.ErrorHandler(func(_ *natsgo.Conn, _ *natsgo.Subscription, err error) {
			c.ins.emitEvent(context.Background(), logging.LevelWarn,
				vocabulary.ActionNATSAsyncError,
				"nats async error",
				logging.Err(err),
			)
		}),
	}
	if cfg.CredsFile != "" {
		opts = append(opts, natsgo.UserCredentials(cfg.CredsFile))
	}

	nc, err := natsgo.Connect(cfg.URL, opts...)
	if err != nil {
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionNATSConnectFailed,
			"nats connect failed",
			logging.String("url", cfg.URL),
			logging.Err(err),
		)
		return nil, fmt.Errorf("nats.New: connect: %w", err)
	}
	c.Conn = nc
	ins.connectionState.Set(1)

	ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionNATSConnected,
		"nats connected",
		logging.String("connected_url", nc.ConnectedUrl()),
		logging.String("client_name", cfg.ClientName),
	)
	return c, nil
}

// Publish sends payload on subject, records the publish outcome + a
// duration observation on the histogram, and emits a DEBUG log on
// success or WARN on failure. Callers should prefer this over the
// embedded Conn.Publish so metrics stay complete.
func (c *Conn) Publish(subject string, data []byte) error {
	start := time.Now()
	err := c.Conn.Publish(subject, data)
	elapsed := time.Since(start)
	kind := classifySubject(subject)
	c.ins.publishDuration.WithLabelValues(kind).Observe(elapsed.Seconds())

	if err != nil {
		c.ins.publishes.WithLabelValues(kind, "failure").Inc()
		c.ins.emitEvent(context.Background(), logging.LevelWarn,
			vocabulary.ActionNATSPublishFailed,
			"nats publish failed",
			logging.String("subject", subject),
			logging.Int("payload_bytes", len(data)),
			logging.Int64("elapsed_us", elapsed.Microseconds()),
			logging.Err(err),
		)
		return err
	}
	c.ins.publishes.WithLabelValues(kind, "success").Inc()
	c.ins.emitEvent(context.Background(), logging.LevelDebug,
		vocabulary.ActionNATSPublish,
		"nats publish ok",
		logging.String("subject", subject),
		logging.Int("payload_bytes", len(data)),
		logging.Int64("elapsed_us", elapsed.Microseconds()),
	)
	return nil
}

// Subscribe registers a callback for messages on subject. The callback
// wrapper increments messages_delivered_total per delivery. Same
// subject-kind classification as Publish keeps cardinality bounded.
//
// Returns the underlying *nats.Subscription so callers can Unsubscribe
// or Drain when needed.
func (c *Conn) Subscribe(subject string, handler natsgo.MsgHandler) (*natsgo.Subscription, error) {
	kind := classifySubject(subject)
	wrapped := func(msg *natsgo.Msg) {
		c.ins.messagesDelivered.WithLabelValues(kind).Inc()
		handler(msg)
	}
	sub, err := c.Conn.Subscribe(subject, wrapped)
	if err != nil {
		c.ins.subscribes.WithLabelValues(kind, "failure").Inc()
		c.ins.emitEvent(context.Background(), logging.LevelError,
			vocabulary.ActionNATSSubscribeFail,
			"nats subscribe failed",
			logging.String("subject", subject),
			logging.Err(err),
		)
		return nil, err
	}
	c.ins.subscribes.WithLabelValues(kind, "success").Inc()
	c.ins.emitEvent(context.Background(), logging.LevelInfo,
		vocabulary.ActionNATSSubscribe,
		"nats subscribe ok",
		logging.String("subject", subject),
	)
	return sub, nil
}

// Close drains any in-flight publishes/subscriptions and closes the
// connection. Emits ActionNATSClosed. Safe to call multiple times —
// nats.Conn.Close is idempotent.
func (c *Conn) Close() {
	c.ins.connectionState.Set(0)
	c.ins.emitEvent(context.Background(), logging.LevelInfo,
		vocabulary.ActionNATSClosed,
		"nats connection closed",
	)
	c.Conn.Close()
}
