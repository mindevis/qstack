// Package version reports build metadata of qStack binaries.
//
// Values are injected at link time:
//
//	go build -ldflags "-X github.com/mindevis/qstack/pkg/version.Version=v2026.10.0 \
//	  -X github.com/mindevis/qstack/pkg/version.Commit=abc1234"
package version

import "fmt"

var (
	// Version is the platform release version (vYYYY.MM.N).
	Version = "dev"
	// Commit is the git commit the binary was built from.
	Commit = "unknown"
)

// String returns a human-readable version line, e.g. "qs-iam dev (unknown)".
func String(binary string) string {
	return fmt.Sprintf("%s %s (%s)", binary, Version, Commit)
}
