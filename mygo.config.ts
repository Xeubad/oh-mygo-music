import release from "./internal/buildinfo/release.json";

// The development app opens links of its own scheme, leaving installed links alone.
export default ({ command }: { command: string }) => ({
  name: "Oh MyGo Music",
  identifier: "dev.egoist.music",
  // slug 与历史更新文件名保持一致，避免旧版更新器找不到新版本资源。
  slug: "mygo-music",
  version: release.version,
  out: "build",
  urlSchemes: [command === "dev" ? "mygo-music-dev" : "mygo-music"],
  updates: {
    publicKey: release.updatePublicKey,
    github: release.repository,
    changelog: "FORK_CHANGELOG.md",
    deltas: 3,
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
