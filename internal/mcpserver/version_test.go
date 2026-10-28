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
	}
	for _, c := range cases {
		if got := buildVersion(c.ldflag, c.info, c.ok); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
