import release from "./internal/buildinfo/release.json";

// The development app opens links of its own scheme, leaving installed links alone.
export default ({ command }: { command: string }) => ({
  name: "MyGo Music",
  identifier: "dev.egoist.music",
  slug: "mygo-music",
  version: release.version,
  out: "build",
  urlSchemes: [command === "dev" ? "mygo-music-dev" : "mygo-music"],
  updates: {
    publicKey: release.updatePublicKey,
    github: release.repository,
    changelog: "FORK_CHANGELOG.md",
    // 首次随包加入模型时，旧版增量几乎等于完整包；后续版本恢复增量更新。
    deltas: release.version === "0.0.6" ? 0 : 3,
  },
  macos: {
    icon: "icon/AppIcon.icon",
    minimumSystemVersion: "13.0",
  },
  linux: {
    comment: "A music player for your files and more",
    categories: ["AudioVideo", "Audio", "Player"],
    webview: false,
  },
});
