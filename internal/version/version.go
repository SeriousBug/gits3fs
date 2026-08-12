// Package version carries the build identity of the binary.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These are set at build time with -ldflags -X.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

func init() {
	if Commit != "" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			Commit = s.Value
		case "vcs.time":
			Date = s.Value
		}
	}
}

// String renders the full version banner.
func String() string {
	s := "git-s3fs " + Version
	if Commit != "" {
		short := Commit
		if len(short) > 12 {
			short = short[:12]
		}
		s += " (" + short + ")"
	}
	if Date != "" {
		s += " built " + Date
	}
	return fmt.Sprintf("%s\n%s %s/%s", s, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
