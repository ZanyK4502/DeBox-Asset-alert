import json
import traceback
import time

from app.db import initialize_database
from app.monitor_service import check_all_rules, send_due_scheduled_reports


INTERVAL_SECONDS = 60


def run() -> None:
    initialize_database()
    while True:
        try:
            result = {
                "rules": check_all_rules(),
                "scheduled_reports": send_due_scheduled_reports(),
            }
            print(json.dumps(result, ensure_ascii=False), flush=True)
        except Exception as exc:
            print(
                json.dumps(
                    {
                        "status": "monitor_error",
                        "error": str(exc),
                        "traceback": traceback.format_exc(limit=5),
                    },
                    ensure_ascii=False,
                ),
                flush=True,
            )
        time.sleep(INTERVAL_SECONDS)


if __name__ == "__main__":
    run()
