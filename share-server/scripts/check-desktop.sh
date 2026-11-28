#!/bin/sh
# 用隔离容器、临时令牌和自制 WAV 验证真实桌面协议，不接触用户的数据卷与凭据。
set -eu
here=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
root=$(dirname "$here")
cd "$here"
umask 077
scratch=$(mktemp -d "$here/.acceptance-XXXXXX")
project="mygo-share-check-$(basename "$scratch" | tr -cd '[:alnum:]' | tr '[:upper:]' '[:lower:]')"
port=${SHARE_CHECK_PORT:-18767}
export SHARE_PORT="$port" SHARE_PUBLIC_ORIGIN="http://127.0.0.1:$port" SHARE_TTL=1h
SHARE_TOKEN=$(openssl rand -hex 32)
export SHARE_TOKEN
compose() { docker compose --project-directory "$here" -f "$here/compose.yaml" --env-file "$scratch/test.env" -p "$project" "$@"; }
cleanup() {
    compose down -v --remove-orphans --rmi local >/dev/null 2>&1 || true
    rm -f -- "$scratch/test.env" "$scratch/tone.wav" "$scratch/clip.mp3"
    rmdir "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
: > "$scratch/test.env"
python3 - "$scratch/tone.wav" <<'PY'
import math, struct, sys, wave
with wave.open(sys.argv[1], 'wb') as f:
    f.setparams((1, 2, 16000, 0, 'NONE', 'not compressed'))
    f.writeframes(b''.join(struct.pack('<h', round(1000 * math.sin(i * 2 * math.pi * 440 / 16000))) for i in range(12 * 16000)))
PY
compose up -d --build --wait --wait-timeout 45
curl -fsS "$SHARE_PUBLIC_ORIGIN/healthz" >/dev/null
cd "$root"
MM_SHARE_ACCEPTANCE_URL="$SHARE_PUBLIC_ORIGIN" MM_SHARE_ACCEPTANCE_TOKEN="$SHARE_TOKEN" \
MM_SHARE_ACCEPTANCE_AUDIO="$scratch/tone.wav" CGO_ENABLED=0 \
    go test -v ./internal/app -run '^TestShareClientProductionContract$' -count=1 -timeout 45s

python3 - "$scratch" <<'PY'
import json, os, pathlib, sys, urllib.request
from urllib.error import HTTPError
origin = os.environ['SHARE_PUBLIC_ORIGIN']
scratch = pathlib.Path(sys.argv[1])

def request(path, method='GET', payload=None, admin=False, extra=None, expected=200):
    headers = dict(extra or {})
    if admin:
        headers['Authorization'] = 'Bearer ' + os.environ['SHARE_TOKEN']
        headers['Origin'] = origin
    req = urllib.request.Request(origin + path, data=payload, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=20) as res:
            assert res.status == expected, (method, path, res.status, expected)
            return res.read()
    except HTTPError as error:
        if error.code != expected:
            raise

body = json.dumps({'title': 'Physical clip check', 'sourceId': 'local', 'trackId': 'tone',
    'quality': 'high', 'template': 'minimal', 'mode': 'clip', 'durationMs': 12000,
    'clipStartMs': 2000, 'clipEndMs': 4000, 'lyrics': [], 'selectedLineIndexes': []}).encode()
draft = json.loads(request('/api/shares', 'POST', body, True,
    {'Content-Type': 'application/json', 'Idempotency-Key': 'physical-clip-check'}, expected=201))
admin = '/api/shares/' + draft['id']
result = json.loads(request(admin + '/audio', 'PUT', (scratch / 'tone.wav').read_bytes(), True,
    {'Content-Type': 'audio/wav'}))
assert result['status'] == 'ready', result['status']
page = '/s/' + draft['id']
assert b'Physical clip check' in request(page)
media = page + '/audio'
assert len(request(media, extra={'Range': 'bytes=0-63'}, expected=206)) == 64
(scratch / 'clip.mp3').write_bytes(request(media))
request(admin, 'DELETE', admin=True, expected=204)
request(page, expected=404)
request(media, expected=404)
print('公开试听成功；Range 返回 206；撤销后页面和媒体均返回 404。')
PY
# ffprobe needs a seekable MP3 file to read the encoded duration.
duration=$(compose exec -T share-server sh -eu -c '
    file=$(mktemp)
    trap '\''rm -f "$file"'\'' EXIT
    cat > "$file"
    ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 "$file"
' < "$scratch/clip.mp3")
python3 -c 'import sys; value=float(sys.argv[1]); assert abs(value-2.0) < 0.100, value; print(f"实际 MP3 时长：{value:.3f}s（原音频 12s，预期片段 2s）")' "$duration"
