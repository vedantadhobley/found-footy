// Package event owns the NATS live-feed publisher and its wire contracts.
// Durable state-transition audit records belong to contract/auditlog and are
// committed inside the owning Postgres repository transaction.
package event
