import os
import signal
import subprocess
import sys
import time


def start(name: str, script: str) -> subprocess.Popen:
    process = subprocess.Popen([sys.executable, script])
    print(f"{name} started pid={process.pid}", flush=True)
    return process


def main() -> None:
    processes: list[tuple[str, subprocess.Popen]] = [("web", start("web", "run.py"))]

    if os.getenv("DEBOX_BOT_RECEIVE_MODE", "polling").strip().lower() == "polling":
        processes.append(("bot", start("bot", "run_bot.py")))

    processes.append(("monitor", start("monitor", "run_monitor.py")))

    try:
        while True:
            for name, process in processes:
                code = process.poll()
                if code is not None:
                    raise RuntimeError(f"{name} exited with code {code}")
            time.sleep(2)
    except KeyboardInterrupt:
        pass
    finally:
        for _, process in processes:
            if process.poll() is None:
                process.send_signal(signal.SIGTERM)
        for _, process in processes:
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()


if __name__ == "__main__":
    main()
