import os
import signal
import subprocess
import sys
import time


RESTARTABLE = {"bot", "monitor"}
RESTART_DELAY_SECONDS = 3


def start(name: str, script: str) -> subprocess.Popen:
    process = subprocess.Popen([sys.executable, script])
    print(f"{name} started pid={process.pid}", flush=True)
    return process


def main() -> None:
    scripts = {"web": "run.py"}

    if os.getenv("DEBOX_BOT_RECEIVE_MODE", "polling").strip().lower() == "polling":
        scripts["bot"] = "run_bot.py"

    scripts["monitor"] = "run_monitor.py"
    processes: dict[str, subprocess.Popen] = {
        name: start(name, script) for name, script in scripts.items()
    }

    try:
        while True:
            for name, process in list(processes.items()):
                code = process.poll()
                if code is not None:
                    if name not in RESTARTABLE:
                        raise RuntimeError(f"{name} exited with code {code}")
                    print(f"{name} exited with code {code}; restarting", flush=True)
                    time.sleep(RESTART_DELAY_SECONDS)
                    processes[name] = start(name, scripts[name])
            time.sleep(2)
    except KeyboardInterrupt:
        pass
    finally:
        for process in processes.values():
            if process.poll() is None:
                process.send_signal(signal.SIGTERM)
        for process in processes.values():
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()


if __name__ == "__main__":
    main()
