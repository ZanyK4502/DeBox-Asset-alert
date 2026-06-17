from __future__ import annotations

import re
import subprocess
import time
from pathlib import Path
from urllib.request import urlopen

from app.bot_service import PUBLIC_URL_PATH
from app.config import ROOT_DIR


LOG_PATH = ROOT_DIR / "data" / "tunnel-manager.log"
CURRENT_LOG_PATH = ROOT_DIR / "data" / "tunnel-current.log"
SSH_PATH = Path(r"C:\Windows\System32\OpenSSH\ssh.exe")
URL_PATTERN = re.compile(r"https://[a-z0-9-]+\.lhr\.life")


def log(message: str) -> None:
    LOG_PATH.parent.mkdir(parents=True, exist_ok=True)
    stamp = time.strftime("%Y-%m-%d %H:%M:%S")
    with LOG_PATH.open("a", encoding="utf-8") as output:
        output.write(f"{stamp} {message}\n")


def healthy(url: str) -> bool:
    try:
        with urlopen(f"{url}/api/health", timeout=15) as response:
            return response.status == 200
    except Exception:
        return False


def publish_url(url: str) -> None:
    PUBLIC_URL_PATH.write_text(url, encoding="utf-8")
    log(f"Published new URL: {url}")


def start_tunnel() -> subprocess.Popen:
    CURRENT_LOG_PATH.write_text("", encoding="utf-8")
    output = CURRENT_LOG_PATH.open("a", encoding="utf-8")
    return subprocess.Popen(
        [
            str(SSH_PATH),
            "-o",
            "StrictHostKeyChecking=accept-new",
            "-o",
            "ServerAliveInterval=30",
            "-o",
            "ExitOnForwardFailure=yes",
            "-R",
            "80:127.0.0.1:8000",
            "nokey@localhost.run",
        ],
        cwd=ROOT_DIR,
        stdout=output,
        stderr=subprocess.STDOUT,
        creationflags=subprocess.CREATE_NO_WINDOW,
    )


def wait_for_url(process: subprocess.Popen) -> str:
    deadline = time.time() + 45
    while time.time() < deadline and process.poll() is None:
        text = CURRENT_LOG_PATH.read_text(encoding="utf-8", errors="ignore")
        match = URL_PATTERN.search(text)
        if match and healthy(match.group(0)):
            return match.group(0)
        time.sleep(2)
    return ""


def run() -> None:
    while True:
        process = start_tunnel()
        url = wait_for_url(process)
        if not url:
            log("Tunnel did not become healthy; restarting")
            process.kill()
            time.sleep(5)
            continue

        publish_url(url)
        failures = 0
        while process.poll() is None:
            time.sleep(30)
            if healthy(url):
                failures = 0
            else:
                failures += 1
                log(f"Health check failed ({failures}/2): {url}")
                if failures >= 2:
                    process.kill()
                    break
        time.sleep(3)


if __name__ == "__main__":
    run()
