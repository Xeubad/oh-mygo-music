"""随产品发行的离线引擎；音乐进入进程之前关闭网络与遥测。"""

import os
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parent
RATE = 44100
for name in ("OMP_NUM_THREADS", "MKL_NUM_THREADS", "OPENBLAS_NUM_THREADS", "NUMBA_NUM_THREADS"):
    os.environ[name] = "2"
os.environ.update({
    "HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1",
    "HF_HUB_DISABLE_TELEMETRY": "1", "DO_NOT_TRACK": "1", "MPLBACKEND": "Agg",
    "PATH": str(ROOT / "bin") + os.pathsep + os.environ.get("PATH", ""),
})


def deny_network(event, arguments):
    if event in {"socket.connect", "socket.getaddrinfo", "socket.sendto", "socket.bind"}:
        raise RuntimeError("AutoMix 音源和特征只允许在本机处理")


sys.addaudithook(deny_network)
import dsp_core as engine
import envelopes as renderer
