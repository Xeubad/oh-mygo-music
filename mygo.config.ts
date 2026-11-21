import release from "./internal/buildinfo/release.json";

// The development app opens links of its own scheme, leaving installed links alone.
export default ({ command }: { command: string }) => ({
  name: "MyGo Music by XEU",
  identifier: "dev.egoist.music",
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
    minimumSystemVersion: "13.0",
  },
  windows: {
    displayName: "MyGo Music by XEU",
    // Preserve existing installs and let the installer migrate the old EXE.
    installDirName: "MyGo Music",
    installerInclude: "scripts/installer.nsh",
  },
  linux: {
    comment: "A music player for your files and more",
    categories: ["AudioVideo", "Audio", "Player"],
    webview: false,
  },
});
