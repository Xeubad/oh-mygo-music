"""从成品归档解压并自检 CPU 助手，防止构建机依赖掩盖运行时缺件。"""

import argparse
import json
import os
from pathlib import Path, PurePosixPath
import subprocess
import tarfile
import tempfile


def verify(archive):
    with tempfile.TemporaryDirectory(prefix="mygo-automix-package-") as temporary:
        root = Path(temporary)
        with tarfile.open(archive, "r:gz") as package:
            for entry in package:
                path = PurePosixPath(entry.name.removeprefix("./"))
                if not path.parts or path.parts[0] != "automix-engine":
                    continue
                if path.is_absolute() or ".." in path.parts or "\\" in entry.name or ":" in entry.name:
                    raise ValueError("分析助手归档含不安全路径")
                if not entry.isdir() and not entry.isfile():
                    raise ValueError("分析助手归档不能包含链接")
                package.extract(entry, root, filter="data")
        engine = root / "automix-engine"
        environment = dict(os.environ)
        for name in ("PYTHONHOME", "PYTHONPATH", "MYGO_AUTOMIX_MODELS"):
            environment.pop(name, None)
        environment["MYGO_AUTOMIX_DEVICE"] = "cpu"
        flags = subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0
        result = subprocess.run(
            [str(engine / "python/python.exe"), str(engine / "worker.py"), "--self-test"],
            cwd=engine, env=environment, capture_output=True, text=True, timeout=120,
            creationflags=flags,
        )
        if result.returncode:
            raise RuntimeError(f"归档内本机助手无法启动：\n{result.stderr}")
        ready = json.loads(result.stdout.strip())
        if ready != {"schema": "adaptive-1", "status": "ready", "device": "cpu"}:
            raise ValueError("归档内本机助手未完成 CPU 自检")
        print(json.dumps({"status": "passed", "archive": archive.name, "helper": ready}), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("archive", type=Path)
    verify(parser.parse_args().archive)
