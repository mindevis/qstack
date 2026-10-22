//go:build linux

package telemetry

import (
	"fmt"
	"net"
	"os"
)

// SDNotify sends a state such as "READY=1" or "WATCHDOG=1" to systemd
// (qs-node and agents run as Type=notify units). It reports false without an
// error when NOTIFY_SOCKET is unset, i.e. the process is not run by systemd.
func SDNotify(state string) (bool, error) {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return false, nil
	}
	if socket[0] == '@' {
		socket = "\x00" + socket[1:] // abstract namespace
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return false, fmt.Errorf("sd_notify: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return false, fmt.Errorf("sd_notify: %w", err)
	}
	return true, nil
}
