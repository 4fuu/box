#!/usr/bin/env python3
"""Set or check the calendar version in VERSION.

The form is YYYY.MDD.REVISION, using the date in Asia/Hong_Kong. The month is
not padded, the day is two digits, and the first release of a day uses
revision zero. 2026.924.0 is the first release on 2026-09-24.
"""

from __future__ import annotations

import re
import sys
from datetime import date
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
VERSION_FILE = ROOT / "VERSION"
VERSION_PATTERN = re.compile(r"^(\d{4})\.(\d{3,4})\.(\d+)$")


def validate_version(version: str) -> None:
    match = VERSION_PATTERN.fullmatch(version)
    if not match:
        raise SystemExit("version must use YYYY.MDD.REVISION, for example 2026.924.0")
    year, month_day, _revision = match.groups()
    month = int(month_day[:-2])
    day = int(month_day[-2:])
    try:
        parsed = date(int(year), month, day)
    except ValueError as error:
        raise SystemExit(f"invalid calendar version {version}: {error}") from error
    if month_day != f"{parsed.month}{parsed.day:02d}":
        raise SystemExit("month must be unpadded and day must be two digits")


def read_version() -> str:
    if not VERSION_FILE.is_file():
        raise SystemExit("VERSION is missing")
    version = VERSION_FILE.read_text().strip()
    validate_version(version)
    return version


def main() -> None:
    if len(sys.argv) == 2 and sys.argv[1] == "--check":
        print(read_version())
        return
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {Path(sys.argv[0]).name} <YYYY.MDD.REVISION> | --check")
    version = sys.argv[1]
    validate_version(version)
    VERSION_FILE.write_text(version + "\n")
    print(f"Set box version to {version}")


if __name__ == "__main__":
    main()
