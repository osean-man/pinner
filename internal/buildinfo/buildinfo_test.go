package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func bi(mainVersion string, settings map[string]string) *debug.BuildInfo {
	b := &debug.BuildInfo{Main: debug.Module{Version: mainVersion}}
	for k, v := range settings {
		b.Settings = append(b.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return b
}

func TestLinkerValuesWin(t *testing.T) {
	got := resolve("v1.2.3", "abcdef0123456789", "2026-10-13T00:00:00Z",
		bi("(devel)", map[string]string{"vcs.revision": "ffffffffffff", "vcs.modified": "true"}))
	if got.Version != "v1.2.3" || got.Commit != "abcdef0123456789" || got.Date != "2026-10-13T00:00:00Z" || got.Dirty {
		t.Fatalf("got %+v", got)
	}
	if s := got.String(); !strings.HasPrefix(s, "v1.2.3 (commit abcdef012345, built 2026-10-13T00:00:00Z, go") {
		t.Fatalf("String() = %q", s)
	}
}

func TestSourceBuildUsesVCSStamp(t *testing.T) {
	got := resolve("", "", "", bi("(devel)", map[string]string{
		"vcs.revision": "0123456789abcdef0123", "vcs.time": "2026-10-13T18:59:45Z", "vcs.modified": "true",
	}))
	if got.Version != "dev" || got.Commit != "0123456789abcdef0123" || got.Date != "2026-10-13T18:59:45Z" || !got.Dirty {
		t.Fatalf("got %+v", got)
	}
	if s := got.String(); !strings.Contains(s, "commit 0123456789ab-dirty") {
		t.Fatalf("String() = %q", s)
	}
}

func TestGoInstallUsesModuleVersion(t *testing.T) {
	got := resolve("", "", "", bi("v1.4.0", nil))
	if got.Version != "v1.4.0" || got.Commit != "unknown" {
		t.Fatalf("got %+v", got)
	}
}

func TestNoBuildInfo(t *testing.T) {
	if got := resolve("", "", "", nil); got.Version != "dev" || got.Commit != "unknown" || got.Date != "unknown" {
		t.Fatalf("got %+v", got)
	}
}
