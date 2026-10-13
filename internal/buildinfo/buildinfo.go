// Package buildinfo describes which build of a binary is running.
//
// Release builds set version, commit and date with -ldflags -X. Builds from source fall back to
// what the Go toolchain stamps into every binary: the module version (for go install pkg@v1.2.3)
// and the git revision of the checkout.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Info is the build identity of a binary.
type Info struct {
	Version string
	Commit  string
	Date    string
	Dirty   bool
}

// Resolve fills in anything not set at link time from the binary's embedded build info.
func Resolve(version, commit, date string) Info {
	bi, _ := debug.ReadBuildInfo()
	return resolve(version, commit, date, bi)
}

func resolve(version, commit, date string, bi *debug.BuildInfo) Info {
	info := Info{Version: version, Commit: commit, Date: date}
	if bi == nil {
		return info.withDefaults()
	}
	if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = s.Value
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = s.Value
			}
		case "vcs.modified":
			// Only meaningful when the commit also came from the build info.
			info.Dirty = s.Value == "true" && commit == ""
		}
	}
	return info.withDefaults()
}

func (i Info) withDefaults() Info {
	if i.Version == "" {
		i.Version = "dev"
	}
	if i.Commit == "" {
		i.Commit = "unknown"
	}
	if i.Date == "" {
		i.Date = "unknown"
	}
	return i
}

// String renders the info for --version output.
func (i Info) String() string {
	commit := i.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if i.Dirty {
		commit += "-dirty"
	}
	return fmt.Sprintf("%s (commit %s, built %s, %s %s/%s)", i.Version, commit, i.Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
