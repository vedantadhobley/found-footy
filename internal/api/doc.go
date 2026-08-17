// Package api is the read-only Chi HTTP surface for fixture, event, search,
// and stable share-ID video reads. Live events flow from workers through NATS
// directly to the vedanta-systems BFF; this package does not use NATS.
package api
