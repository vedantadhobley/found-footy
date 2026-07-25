// Postgres-adapter Action enum values + init-time registration.
package vocabulary

// Postgres-adapter actions. Cover the pool lifecycle + per-query
// outcomes. Query-level actions get emitted at DEBUG by default;
// the pool actions at INFO. See docs/design/rebuild-plan.md §11 vocabulary.
const (
	ActionPoolConnected     Action = "pool_connected"      // pgxpool built + Ping succeeded
	ActionPoolConnectFailed Action = "pool_connect_failed" // pgxpool construction or Ping failed
	ActionPoolClosed        Action = "pool_closed"         // pgxpool.Close() invoked
	ActionQuery             Action = "query"               // SELECT/INSERT/UPDATE issued successfully
	ActionQueryFailed       Action = "query_failed"        // query returned pgconn.PgError or transport error
	ActionPing              Action = "ping"                // health probe succeeded
	ActionPingFailed        Action = "ping_failed"         // health probe failed
)

func init() {
	registerActions(
		ActionPoolConnected,
		ActionPoolConnectFailed,
		ActionPoolClosed,
		ActionQuery,
		ActionQueryFailed,
		ActionPing,
		ActionPingFailed,
	)
}
