// Configuration tests pin required secrets, bounded polling, and defaults.
package config

import (
	"strings"
	"testing"
	"time"
)

// TestValidate accepts the smallest complete runtime contract.
func TestValidate(t *testing.T) {
	c := Config{
		PollInterval: time.Second, ResyncInterval: time.Minute, PGMaxConns: 4,
		PGDSN: "postgres://db", S3Endpoint: "http://garage:3900", S3Bucket: "footy",
		S3AccessKey: "access", S3SecretKey: "secret",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestValidateRejectsIncompleteConfig verifies startup fails before partial service.
func TestValidateRejectsIncompleteConfig(t *testing.T) {
	c := Config{PollInterval: time.Second, ResyncInterval: time.Minute, PGMaxConns: 4}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("Validate error = %v, want required-field failure", err)
	}
}
