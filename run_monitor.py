import time

from app.db import initialize_database
from app.monitor_service import check_all_rules


def run() -> None:
    initialize_database()
    while True:
        print(check_all_rules(), flush=True)
        time.sleep(30)


if __name__ == "__main__":
    run()
