package version

import "testing"

func TestString(t *testing.T) {
	tests := []struct {
		name    string
		version string
		commit  string
		binary  string
		want    string
	}{
		{name: "defaults", version: "dev", commit: "unknown", binary: "qs-iam", want: "qs-iam dev (unknown)"},
		{name: "release", version: "v2026.10.0", commit: "abc1234", binary: "qs", want: "qs v2026.10.0 (abc1234)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version, Commit = tt.version, tt.commit
			t.Cleanup(func() { Version, Commit = "dev", "unknown" })

			if got := String(tt.binary); got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.binary, got, tt.want)
			}
		})
	}
}
