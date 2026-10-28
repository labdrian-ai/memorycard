package mcpserver

import (
	"runtime/debug"
	"testing"
)

func TestBuildVersionPrefersTheReleaseTagThenTheModuleVersion(t *testing.T) {
	module := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	cases := []struct {
		name   string
		ldflag string
		info   *debug.BuildInfo
		ok     bool
		want   string
	}{
		{"release build keeps the tag", "0.1.1", module("v0.1.1"), true, "0.1.1"},
		{"go install reports the module version", devVersion, module("v0.1.1"), true, "0.1.1"},
		{"unversioned build stays dev", devVersion, module("(devel)"), true, devVersion},
		{"no module version stays dev", devVersion, module(""), true, devVersion},
		{"no build info stays dev", devVersion, nil, false, devVersion},
		{"go install of a prerelease tag", devVersion, module("v0.2.0-rc.1"), true, "0.2.0-rc.1"},
		{"local build with uncommitted changes stays dev", devVersion, module("v0.1.1+dirty"), true, devVersion},
		{"local build past the last tag stays dev", devVersion, module("v0.1.2-0.20261028190000-3f5b8cb0cd13"), true, devVersion},
		{"local dirty build past a prerelease stays dev", devVersion, module("v0.2.0-rc.1.0.20261028190000-3f5b8cb0cd13+dirty"), true, devVersion},
	}
	for _, c := range cases {
		if got := buildVersion(c.ldflag, c.info, c.ok); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
