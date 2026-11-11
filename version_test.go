package main

import (
	"runtime/debug"
	"testing"
)

func TestFormatVersion(t *testing.T) {
	rev := debug.BuildSetting{Key: "vcs.revision", Value: "8a2d7ee5014cd90f8c482934db941f51cfd3a286"}
	clean := debug.BuildSetting{Key: "vcs.modified", Value: "false"}
	dirty := debug.BuildSetting{Key: "vcs.modified", Value: "true"}

	tests := []struct {
		name     string
		version  string
		settings []debug.BuildSetting
		want     string
	}{
		{"tag and commit", "v0.1.0", []debug.BuildSetting{rev, clean}, "v0.1.0 (8a2d7ee)"},
		{"dirty tree", "dev", []debug.BuildSetting{rev, dirty}, "dev (8a2d7ee-dirty)"},
		{"no vcs info", "v0.1.0", nil, "v0.1.0"},
		{"short revision", "dev", []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}, "dev (abc)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatVersion(tt.version, tt.settings); got != tt.want {
				t.Errorf("formatVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}
