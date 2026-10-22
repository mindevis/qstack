//go:build linux

package telemetry

import (
	"net"
	"path/filepath"
	"testing"
)

func TestSDNotify(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if sent, err := SDNotify("READY=1"); sent || err != nil {
		t.Errorf("without NOTIFY_SOCKET: sent=%v err=%v", sent, err)
	}

	sock := filepath.Join(t.TempDir(), "notify.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	t.Setenv("NOTIFY_SOCKET", sock)

	if sent, err := SDNotify("READY=1"); !sent || err != nil {
		t.Fatalf("SDNotify: sent=%v err=%v", sent, err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || string(buf[:n]) != "READY=1" {
		t.Errorf("received %q, %v", buf[:n], err)
	}
}
