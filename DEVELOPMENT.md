# Development

[返回项目首页 / Back to MyGo Music by XEU](README.md) · [插件开发 / Plugin development](PLUGIN_DEVELOPMENT.md)

Go 版本要求见 [go.mod](go.mod)。下方保留原 README 的构建与播客转写运行库维护说明；普通使用者请从[发行页](https://github.com/zxbdzh/oh-mygo-music/releases/latest)下载安装。

See [go.mod](go.mod) for the required Go version. The build and podcast-runtime maintenance instructions below are preserved from the original README. For everyday use, download an app package from [Releases](https://github.com/zxbdzh/oh-mygo-music/releases/latest).

## Build

```bash
go tool mygo dev     # run in development
go tool mygo build   # package into build/
go test ./internal/...
```

## Podcast captions

Episodes are written down on the device as they play, with [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) and Qwen3-ASR, which the app downloads when the listener asks for them. The libraries come from this repository's `sherpa-onnx-<version>` prerelease, which only changes with that version:

```bash
go run ./scripts/sherpa -sign "Developer ID Application: …"
gh release create sherpa-onnx-2.0.1 --prerelease --latest=false build/sherpa/*
```
