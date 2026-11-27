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
	// 翻页是 fork 的扩展，保留真实上游基线并用第四段区分旧宿主。
	if release.UpstreamVersion == "0.1.26" {
		return "0.1.26.1"
	}
	return release.UpstreamVersion
}
