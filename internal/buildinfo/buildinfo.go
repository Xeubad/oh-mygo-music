package buildinfo

import (
	_ "embed"
	"encoding/json"
)

//go:embed release.json
var releaseJSON []byte

// PluginVersion returns the official version whose plugin APIs this fork supports.
func PluginVersion() string {
	var release struct {
		UpstreamVersion string `json:"upstreamVersion"`
	}
	if err := json.Unmarshal(releaseJSON, &release); err != nil {
		panic(err)
	}
	if release.UpstreamVersion == "" {
		panic("release.json is missing upstreamVersion")
	}
	return release.UpstreamVersion
}
