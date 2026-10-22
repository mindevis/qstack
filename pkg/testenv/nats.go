package testenv

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
)

// NATSURLEnv names the variable with the URL of an existing NATS server with
// JetStream enabled.
const NATSURLEnv = "QS_TEST_NATS_URL"

// natsImage is the NATS server version deployed with the control plane.
const natsImage = "nats:2.15.1"

var (
	natsOnce sync.Once
	natsURL  string
	natsErr  error
)

// NATS returns the URL of a NATS server with JetStream. The server is shared:
// tests isolate themselves with unique stream and subject names (Name).
func NATS(t testing.TB) string {
	t.Helper()
	if url := os.Getenv(NATSURLEnv); url != "" {
		return url
	}
	natsOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		c, err := tcnats.Run(ctx, natsImage) // started with -js
		if err != nil {
			natsErr = fmt.Errorf("start %s container: %w", natsImage, err)
			return
		}
		natsURL, natsErr = c.ConnectionString(ctx)
	})
	if natsErr != nil {
		t.Fatalf("testenv: no NATS: %v (set %s or run Docker)", natsErr, NATSURLEnv)
	}
	return natsURL
}

// Name returns prefix followed by a random suffix, usable as a NATS subject
// token or stream name.
func Name(prefix string) string { return prefix + randomSuffix() }
