//go:build integration

package testenv

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestNATSHasJetStream(t *testing.T) {
	nc, err := nats.Connect(NATS(t))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AccountInfo(context.Background()); err != nil {
		t.Fatalf("JetStream not available: %v", err)
	}
}
