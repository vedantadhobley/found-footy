// Package pg wraps pgx: connection pool, transaction helpers (WithTx +
// WithRetryableTx for REPEATABLE READ + serialization retry), typed
// error classification. See §9 pg adapter.
package pg
