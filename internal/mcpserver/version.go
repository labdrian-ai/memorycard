package mcpserver

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// devVersion is the version of a build that no release tag stamped.
const devVersion = "dev"

// Version is reported to MCP clients and by `memorycard version`. Release
// builds set it from the git tag with -ldflags "-X ...mcpserver.Version=...";
// `go install ...@vX.Y.Z` builds take it from the module version instead.
var Version = devVersion

func init() {
	info, ok := debug.ReadBuildInfo()
	Version = buildVersion(Version, info, ok)
}

// pseudoVersion matches the timestamp-and-commit suffix Go gives a module
// version that is not a tag, as in v0.1.2-0.20261028190000-3f5b8cb0cd13.
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)

// buildVersion returns the version stamped by the linker, else the module
// version Go records for `go install` of a tag, without its leading "v".
// A local build in a git checkout also gets a module version, but a pseudo
// version or a "+dirty" one names no release, so it stays dev.
func buildVersion(ldflag string, info *debug.BuildInfo, ok bool) string {
	if ldflag != devVersion || !ok || info == nil {
		return ldflag
	}
	v, _, meta := strings.Cut(info.Main.Version, "+")
	if v == "" || v == "(devel)" || meta || pseudoVersion.MatchString(v) {
		return ldflag
	}
	return strings.TrimPrefix(v, "v")
}
