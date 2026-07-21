from __future__ import annotations


DEFAULT_LANGUAGE = "zh"
SUPPORTED_LANGUAGES = frozenset({"zh", "en"})


def normalize_language(value: str | None) -> str:
    language = str(value or "").strip().lower()
    return language if language in SUPPORTED_LANGUAGES else DEFAULT_LANGUAGE


def require_language(value: str | None) -> str:
    language = str(value or "").strip().lower()
    if language not in SUPPORTED_LANGUAGES:
        raise ValueError("语言只能是 zh 或 en。")
    return language
