"""构建无需用户安装 Python/CUDA 的 Windows 本机 AutoMix 运行时。"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
MODELS = {
    "small0.ckpt": (
        "https://cloud.cp.jku.at/public.php/dav/files/7ik4RrBKTS273gp/small0.ckpt",
        "6074be2c4d490c5f6101fcc374a1ec72ae93456e23bb6019783b849f5dc7d47b",
    ),
    "955717e8-8726e21a.th": (
        "https://dl.fbaipublicfiles.com/demucs/hybrid_transformer/955717e8-8726e21a.th",
        "8726e21a993978c7ba086d3872e7608d7d5bfca646ca4aca459ffda844faa8b4",
    ),
}


def install_packages(arguments, site):
    if shutil.which("uv"):
        command = ["uv", "pip", "install", "--python", sys.executable, "--target", str(site)]
    else:
        command = [sys.executable, "-m", "pip", "install", "--disable-pip-version-check", "--target", str(site)]
    subprocess.run([*command, "--only-binary=:all:", *arguments], check=True)


def download(url, path):
    if not path.exists():
        path.parent.mkdir(parents=True, exist_ok=True)
        temporary = path.with_suffix(".part")
        subprocess.run([
            "curl.exe" if os.name == "nt" else "curl", "--fail", "--location", "--retry", "3",
            "--connect-timeout", "15", "--max-time", "180", "--output", str(temporary), url,
        ], check=True)
        temporary.replace(path)
    return path


def stage_windows_dlls(site):
    package = site / "sklearn"
    initializer = package / "_distributor_init.py"
    source = initializer.read_text(encoding="utf-8")
    if '".libs"' not in source and '"_runtime_libs"' not in source:
        raise RuntimeError("scikit-learn 的 Windows DLL 加载方式已变化")
    # MyGo 不打包隐藏目录；使用普通目录保留上游自带的 DLL 和完整绝对路径加载。
    libraries = package / "_runtime_libs"
    libraries.mkdir(exist_ok=True)
    for name in ("vcomp140.dll", "msvcp140.dll"):
        original = package / ".libs" / name
        target = libraries / name
        if original.is_file():
            shutil.copy2(original, target)
        if not target.is_file():
            raise RuntimeError(f"本机分析运行时缺少 {name}")
    initializer.write_text(source.replace('".libs"', '"_runtime_libs"'), encoding="utf-8")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, default=ROOT / "resources/windows/automix-engine")
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--model-cache", type=Path)
    parser.add_argument("--rubberband-cache", type=Path)
    arguments = parser.parse_args()
    output, cache = arguments.output.resolve(), arguments.cache.resolve()
    output.mkdir(parents=True, exist_ok=True)
    cache.mkdir(parents=True, exist_ok=True)
    python_root = output / "python"
    if not (python_root / "python.exe").exists():
        archive = download("https://www.python.org/ftp/python/3.11.9/python-3.11.9-embed-amd64.zip",
                           cache / "python-3.11.9-embed-amd64.zip")
        with zipfile.ZipFile(archive) as package:
            package.extractall(python_root)
    site = python_root / "Lib/site-packages"
    if not (site / "torch/__init__.py").exists():
        install_packages([
            "torch==2.8.0", "torchaudio==2.8.0",
            "--index-url", "https://download.pytorch.org/whl/cpu",
        ], site)
    if not (site / "beat_this/__init__.py").exists():
        install_packages([
            "numpy==2.4.6", "scipy==1.17.1", "scikit-learn==1.9.1",
            "soundfile==0.14.0", "librosa==0.11.0", "beat-this==1.1.0",
            "demucs-infer==4.4.0", "pyloudnorm==0.2.0",
            "--extra-index-url", "https://download.pytorch.org/whl/cpu",
            "--constraint", str(ROOT / "engine/automix/constraints.txt"),
        ], site)
    stage_windows_dlls(site)
    # 推理只加载 DLL；开发用 dnnl.lib 等静态库体积很大，还会耗尽 32 位 NSIS 的地址空间。
    for directory in (site / "torch/lib", site / "torchaudio/lib"):
        for pattern in ("*.lib", "*.pdb"):
            for library in directory.glob(pattern):
                if library.is_file() and library.resolve().is_relative_to(output):
                    library.unlink()
    # 嵌入式 Python 默认隔离搜索路径，只加入随包代码和随包依赖。
    (python_root / "python311._pth").write_text(
        "python311.zip\n.\nLib/site-packages\n..\nimport site\n", encoding="utf-8"
    )
    models = output / "models"
    models.mkdir(exist_ok=True)
    for name, (url, expected) in MODELS.items():
        target = models / name
        local = arguments.model_cache / name if arguments.model_cache else None
        if not target.exists():
            if local is not None and local.exists():
                shutil.copy2(local, target)
            else:
                shutil.copy2(download(url, cache / name), target)
        if hashlib.sha256(target.read_bytes()).hexdigest() != expected:
            raise RuntimeError(f"模型 SHA-256 不符: {name}")
    binary = output / "bin"
    binary.mkdir(exist_ok=True)
    rubberband = arguments.rubberband_cache
    if rubberband is None:
        archive = download(
            "https://breakfastquay.com/files/releases/rubberband-4.0.0-gpl-executable-windows.zip",
            cache / "rubberband-4.0.0-gpl-executable-windows.zip",
        )
        directory = cache / "rubberband"
        with zipfile.ZipFile(archive) as package:
            package.extractall(directory)
        matches = list(directory.glob("*/rubberband.exe")) + list(directory.glob("rubberband.exe"))
        if len(matches) != 1:
            raise RuntimeError("Rubber Band 包结构不符")
        rubberband = matches[0].parent
    for name in ("rubberband.exe", "sndfile.dll", "COPYING.txt", "README.txt"):
        shutil.copy2(rubberband / name, binary / name)
    for url, name in (
        ("https://breakfastquay.com/files/releases/rubberband-4.0.0.tar.bz2", "rubberband-4.0.0-source.tar.bz2"),
        ("https://github.com/libsndfile/libsndfile/releases/download/1.2.2/libsndfile-1.2.2.tar.xz", "libsndfile-1.2.2-source.tar.xz"),
        ("https://raw.githubusercontent.com/libsndfile/libsndfile/1.2.2/COPYING", "LICENSE-libsndfile"),
    ):
        shutil.copy2(download(url, cache / name), binary / name)
    for source in (ROOT / "engine/automix").iterdir():
        if source.is_file() and (source.suffix in (".py", ".txt") or source.name.startswith("LICENSE")):
            shutil.copy2(source, output / source.name)
    subprocess.run([str(python_root / "python.exe"), str(output / "worker.py"), "--self-test"],
                   cwd=output, check=True)
    manifest = {
        "schema": "adaptive-1", "runtime": "CPython 3.11.9 Windows x64; CPU PyTorch",
        "arm64": "Windows 11 x64 emulation; unavailable runtimes retain original playback",
        "models": {name: value[1] for name, value in MODELS.items()},
        "source": "engine/automix in the matching release tag",
    }
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2), encoding="utf-8")
    print(json.dumps({"status": "ready", "path": str(output)}), flush=True)


if __name__ == "__main__":
    main()
