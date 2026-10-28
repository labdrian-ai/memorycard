package mcpserver

import (
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

// buildVersion returns the version stamped by the linker, else the module
// version Go records for `go install`, without its leading "v".
func buildVersion(ldflag string, info *debug.BuildInfo, ok bool) string {
	if ldflag != devVersion || !ok || info == nil {
		return ldflag
	}
	switch v := info.Main.Version; v {
	case "", "(devel)":
		return ldflag
	default:
		return strings.TrimPrefix(v, "v")
	}
}
