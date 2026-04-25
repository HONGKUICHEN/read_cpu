#!/usr/bin/env python3
"""
Monitor CPU and memory usage around daily UTC midnight.

Sampling window:
  - start: 23:59:00 UTC of each day
  - end:   00:05:00 UTC of the next day

Default sampling interval:
  - 1 second

Outputs:
  - logs/YYYY-MM-DD.csv
  - logs/YYYY-MM-DD.jsonl

The date in the filename is the UTC date of the 00:00:00 boundary. Example:
  - window 2026-04-25 23:59:00 UTC -> 2026-04-26 00:05:00 UTC
  - file name: logs/2026-04-26.csv
"""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import pathlib
import signal
import sys
import time
from dataclasses import dataclass
from typing import Dict, Tuple


UTC = dt.timezone.utc
DEFAULT_INTERVAL_SECONDS = 1.0
WINDOW_START = dt.time(hour=23, minute=59, second=0)
WINDOW_END = dt.time(hour=0, minute=5, second=0)


@dataclass
class CpuTimes:
    total: int
    idle: int


@dataclass
class MemorySnapshot:
    mem_total_kb: int
    mem_available_kb: int
    mem_used_kb: int
    mem_used_percent: float
    swap_total_kb: int
    swap_free_kb: int
    swap_used_kb: int
    swap_used_percent: float


STOP = False


def handle_signal(signum: int, _frame: object) -> None:
    global STOP
    STOP = True
    print(f"received signal {signum}, stopping", file=sys.stderr)


def register_signal_handlers() -> None:
    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)


def read_cpu_times() -> CpuTimes:
    with open("/proc/stat", "r", encoding="utf-8") as f:
        first = f.readline().strip()
    parts = first.split()
    if len(parts) < 8 or parts[0] != "cpu":
        raise RuntimeError(f"unexpected /proc/stat first line: {first!r}")
    values = [int(x) for x in parts[1:]]
    idle = values[3] + values[4]  # idle + iowait
    total = sum(values)
    return CpuTimes(total=total, idle=idle)


def cpu_percent(prev: CpuTimes, curr: CpuTimes) -> float:
    total_delta = curr.total - prev.total
    idle_delta = curr.idle - prev.idle
    if total_delta <= 0:
        return 0.0
    used = total_delta - idle_delta
    return max(0.0, min(100.0, used * 100.0 / total_delta))


def read_meminfo() -> Dict[str, int]:
    data: Dict[str, int] = {}
    with open("/proc/meminfo", "r", encoding="utf-8") as f:
        for line in f:
            name, raw_value = line.split(":", 1)
            value_part = raw_value.strip().split()[0]
            data[name] = int(value_part)
    return data


def read_memory_snapshot() -> MemorySnapshot:
    info = read_meminfo()
    mem_total = info["MemTotal"]
    mem_available = info["MemAvailable"]
    mem_used = mem_total - mem_available
    mem_used_percent = 0.0 if mem_total == 0 else mem_used * 100.0 / mem_total

    swap_total = info.get("SwapTotal", 0)
    swap_free = info.get("SwapFree", 0)
    swap_used = swap_total - swap_free
    swap_used_percent = 0.0 if swap_total == 0 else swap_used * 100.0 / swap_total

    return MemorySnapshot(
        mem_total_kb=mem_total,
        mem_available_kb=mem_available,
        mem_used_kb=mem_used,
        mem_used_percent=mem_used_percent,
        swap_total_kb=swap_total,
        swap_free_kb=swap_free,
        swap_used_kb=swap_used,
        swap_used_percent=swap_used_percent,
    )


def current_utc() -> dt.datetime:
    return dt.datetime.now(UTC)


def window_for_boundary(boundary_date: dt.date) -> Tuple[dt.datetime, dt.datetime]:
    window_end = dt.datetime.combine(boundary_date, WINDOW_END, tzinfo=UTC)
    window_start = window_end - dt.timedelta(minutes=6)
    return window_start, window_end


def active_boundary_date(now: dt.datetime) -> dt.date | None:
    today_start, today_end = window_for_boundary(now.date())
    if today_start <= now <= today_end:
        return now.date()

    tomorrow = now.date() + dt.timedelta(days=1)
    tomorrow_start, tomorrow_end = window_for_boundary(tomorrow)
    if tomorrow_start <= now <= tomorrow_end:
        return tomorrow

    return None


def next_window_start(now: dt.datetime) -> dt.datetime:
    today_start, today_end = window_for_boundary(now.date())
    if now < today_start:
        return today_start
    if today_start <= now <= today_end:
        return now
    tomorrow = now.date() + dt.timedelta(days=1)
    tomorrow_start, _ = window_for_boundary(tomorrow)
    return tomorrow_start


def ensure_log_paths(log_dir: pathlib.Path, boundary_date: dt.date) -> Tuple[pathlib.Path, pathlib.Path]:
    log_dir.mkdir(parents=True, exist_ok=True)
    base = boundary_date.isoformat()
    return log_dir / f"{base}.csv", log_dir / f"{base}.jsonl"


def write_csv_header_if_needed(path: pathlib.Path) -> None:
    if path.exists():
        return
    with open(path, "w", newline="", encoding="utf-8") as f:
        writer = csv.writer(f)
        writer.writerow(
            [
                "timestamp_utc",
                "cpu_percent",
                "mem_used_percent",
                "mem_total_kb",
                "mem_available_kb",
                "mem_used_kb",
                "swap_total_kb",
                "swap_free_kb",
                "swap_used_kb",
                "swap_used_percent",
            ]
        )


def append_sample(csv_path: pathlib.Path, jsonl_path: pathlib.Path, sample: Dict[str, object]) -> None:
    write_csv_header_if_needed(csv_path)

    with open(csv_path, "a", newline="", encoding="utf-8") as f:
        writer = csv.writer(f)
        writer.writerow(
            [
                sample["timestamp_utc"],
                sample["cpu_percent"],
                sample["mem_used_percent"],
                sample["mem_total_kb"],
                sample["mem_available_kb"],
                sample["mem_used_kb"],
                sample["swap_total_kb"],
                sample["swap_free_kb"],
                sample["swap_used_kb"],
                sample["swap_used_percent"],
            ]
        )

    with open(jsonl_path, "a", encoding="utf-8") as f:
        f.write(json.dumps(sample, ensure_ascii=True) + "\n")


def sample_window(boundary_date: dt.date, interval_seconds: float, log_dir: pathlib.Path) -> None:
    csv_path, jsonl_path = ensure_log_paths(log_dir, boundary_date)
    window_start, window_end = window_for_boundary(boundary_date)

    print(
        f"sampling UTC window for {boundary_date.isoformat()}: "
        f"{window_start.isoformat()} -> {window_end.isoformat()}",
        flush=True,
    )

    prev_cpu = read_cpu_times()
    next_tick = time.time()

    while not STOP:
        now = current_utc()
        if now > window_end:
            print(f"finished window {boundary_date.isoformat()}", flush=True)
            return

        sleep_for = next_tick - time.time()
        if sleep_for > 0:
            time.sleep(sleep_for)

        now = current_utc()
        curr_cpu = read_cpu_times()
        mem = read_memory_snapshot()
        sample = {
            "timestamp_utc": now.replace(microsecond=0).isoformat().replace("+00:00", "Z"),
            "cpu_percent": round(cpu_percent(prev_cpu, curr_cpu), 2),
            "mem_used_percent": round(mem.mem_used_percent, 2),
            "mem_total_kb": mem.mem_total_kb,
            "mem_available_kb": mem.mem_available_kb,
            "mem_used_kb": mem.mem_used_kb,
            "swap_total_kb": mem.swap_total_kb,
            "swap_free_kb": mem.swap_free_kb,
            "swap_used_kb": mem.swap_used_kb,
            "swap_used_percent": round(mem.swap_used_percent, 2),
        }
        append_sample(csv_path, jsonl_path, sample)
        prev_cpu = curr_cpu
        next_tick += interval_seconds


def run_forever(interval_seconds: float, log_dir: pathlib.Path) -> None:
    last_completed_boundary: dt.date | None = None

    while not STOP:
        now = current_utc()
        boundary_date = active_boundary_date(now)

        if boundary_date is not None and boundary_date != last_completed_boundary:
            sample_window(boundary_date, interval_seconds, log_dir)
            last_completed_boundary = boundary_date
            continue

        wake_at = next_window_start(now)
        sleep_seconds = max(1.0, min((wake_at - now).total_seconds(), 60.0))
        print(
            f"idle until next UTC window, now={now.isoformat()} wake_in={sleep_seconds:.0f}s",
            flush=True,
        )
        time.sleep(sleep_seconds)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Read CPU and memory usage from 23:59 UTC to 00:05 UTC every day."
    )
    parser.add_argument(
        "--interval-seconds",
        type=float,
        default=DEFAULT_INTERVAL_SECONDS,
        help="Sampling interval in seconds. Default: 1.0",
    )
    parser.add_argument(
        "--log-dir",
        default=str(pathlib.Path(__file__).resolve().parent / "logs"),
        help="Directory for CSV and JSONL output files.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.interval_seconds <= 0:
        print("--interval-seconds must be > 0", file=sys.stderr)
        return 2

    register_signal_handlers()
    log_dir = pathlib.Path(args.log_dir)
    run_forever(interval_seconds=args.interval_seconds, log_dir=log_dir)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
