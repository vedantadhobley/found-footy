// Package config parses environment variables into typed structs via
// envconfig. Each adapter has its own Config struct; the top-level
// Load() aggregates them. See §9 adapter Config blocks.
package config
