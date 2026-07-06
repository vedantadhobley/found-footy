// Package errors is the typed-error taxonomy that adapter packages
// import + build on. Common error kinds (NotFound, Timeout,
// InvalidTransition, ...) live here; adapter-specific errors live in
// each adapter package (e.g. syndication.ErrGeoRestricted).
package errors
