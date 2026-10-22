//go:build !linux

package telemetry

// SDNotify is a no-op outside Linux: systemd notification is Linux-only.
func SDNotify(string) (bool, error) { return false, nil }
