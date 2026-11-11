package main

import "runtime/debug"

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// versionString returns the version with the git commit that Go embeds in
// builds made inside a checkout, for example "v0.1.0 (8a2d7ee)".
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	return formatVersion(version, info.Settings)
}

// formatVersion appends the short vcs.revision, plus "-dirty" when
// vcs.modified is true. Without a revision it returns v unchanged.
func formatVersion(v string, settings []debug.BuildSetting) string {
	var rev string
	var dirty bool
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return v
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "-dirty"
	}
	return v + " (" + rev + ")"
}
