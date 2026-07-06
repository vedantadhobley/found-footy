// Package vocabulary is the compile-time contract for structured logging:
// typed Module + Action enums that logging.Emit accepts. Adding a new
// (module, action) pair is a source-code edit; drift is a compile error,
// not a runtime "why isn't this indexed in Loki." See §11 vocabulary.
package vocabulary
