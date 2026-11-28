# XEU 分享服务器

给 MyGo Music by XEU 用的独立分享服务。桌面端上传有权分享的曲目，接收方打开链接就能试听，不需要登录原音源。服务端使用独立 Go 模块，桌面端无需重新编译或更换接口。

## 用 Docker Compose 本机运行

在本目录执行，需要 Docker 和 Compose：

```sh
umask 077
if [ ! -e .env ]; then
  { printf 'SHARE_TOKEN='; openssl rand -hex 32; } > .env
fi
docker compose up -d --build
curl -fsS http://127.0.0.1:8080/healthz
```

仅首次生成 `.env`，不要覆盖已有配置。默认只映射本机 `127.0.0.1:8080`，重启后共享数据留在 Docker 命名卷 `share-data`（`docker compose down -v` 会删除该卷）。`.env` 是管理凭据，不要提交或分享。可参考 `.env.example` 修改地址、端口和过期时长；修改本机端口时同时更新 `SHARE_PUBLIC_ORIGIN`。

在桌面端「设置 → 分享」填服务器地址 `http://127.0.0.1:8080`，并保存 `.env` 中的管理令牌。普通歌曲从「分享卡片」复制试听链接，本机打开即可验收。**回环地址只在运行服务的那台机器可用，手机扫码不会连到这台机器。**

## 从源码运行

需要 Go 与 `ffmpeg`（PATH 可发现）。在本目录运行：

```sh
set -a; . ./.env; set +a
go run ./cmd/share-server
```

默认监听 `127.0.0.1:8080`，持久化目录为 `./data`，分享有效期 `168h`。可用 `SHARE_LISTEN`、`SHARE_DATA_DIR`、`SHARE_TTL` 覆盖。也可以用 `SHARE_TOKEN_FILE` 指向受限权限的凭据文件，在这种用法下不设置 `SHARE_TOKEN`。

## 部署到公网

1. 给分享服务单独的域名，设置 `SHARE_PUBLIC_ORIGIN` 为该 HTTPS 源地址（不要附加路径）。
2. 在宿主机用 HTTPS 反代转发到 `127.0.0.1:8080`，转发 `/api`、`/s` 及同域资源，放行音频上传和带 `Range` 的试听请求。音频请求体上限 200 MiB，转码最长 5 分钟；按上传带宽配置反代超时，不缓存分享页或媒体响应。反代不记录管理令牌，若访问日志记录路径，应按敏感数据保护分享 ID。
3. 用新的公网 HTTPS 地址在桌面端保存令牌，再从另一台设备核对试听和二维码；管理令牌不进入链接。当前未部署公网实例。

首版用于个人单实例；管理令牌拥有全部分享管理权限。分享链接是持链接可访问，不是私人账号授权；撤销无法收回对方已保存的文件。只上传自己拥有分发权的内容；建议给数据卷设磁盘配额并纳入备份，勿把数据目录直接交给静态文件服务器。

## 开发与验证

```sh
go test ./...
```

在本目录执行 `sh scripts/check-desktop.sh` 可复跑桌面协议验收（依赖 Docker Compose、Go、Python 3、OpenSSL 和 curl）。脚本用独立容器、临时令牌和生成的测试音频，不读取已有 `.env` 或操作用户数据卷，退出时清理测试资源。该用例覆盖整曲、片段、分享复用和逐字歌词时间。

也可以从仓库根目录运行 `go test ./internal/app -run '^TestShareClientProductionContract$' -count=1`，显式设置 `MM_SHARE_ACCEPTANCE_URL`、`MM_SHARE_ACCEPTANCE_TOKEN`、`MM_SHARE_ACCEPTANCE_AUDIO` 指向独立验收服务和测试音频。

服务的职责与数据流见 [ARCHITECTURE.md](ARCHITECTURE.md)。
