#!/usr/bin/env python3
"""Benchmark episode catalog sort/filter shapes without embedding credentials.

Required environment:
  SILO_BASE_URL        Example: https://silo.example.com
  SILO_BEARER_TOKEN    API access token
  SILO_PROFILE_ID      Profile id used for personalized filters/sorts

Optional environment:
  SILO_LIBRARY_ID      Defaults to 2
"""

from __future__ import annotations

import argparse
import csv
import json
import os
import statistics
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from typing import Iterable


def group_rule(field: str, op: str, value: str) -> dict[str, str]:
    return {
        "groups[0][match]": "all",
        "groups[0][rules][0][field]": field,
        "groups[0][rules][0][op]": op,
        "groups[0][rules][0][value]": value,
    }


DEFAULT_SORT_CASES = [
    ("sort:title", {"sort": "title", "order": "asc"}),
    ("sort:added_at", {"sort": "added_at", "order": "desc"}),
    ("sort:release_date", {"sort": "release_date", "order": "desc"}),
    ("sort:last_air_date", {"sort": "last_air_date", "order": "desc"}),
    ("sort:year", {"sort": "year", "order": "desc"}),
    ("sort:content_rating", {"sort": "content_rating", "order": "asc"}),
    ("sort:runtime", {"sort": "runtime", "order": "desc"}),
    ("sort:rating_imdb", {"sort": "rating_imdb", "order": "desc"}),
    ("sort:rating_tmdb", {"sort": "rating_tmdb", "order": "desc"}),
    ("sort:rating_rt_critic", {"sort": "rating_rt_critic", "order": "desc"}),
    ("sort:rating_rt_audience", {"sort": "rating_rt_audience", "order": "desc"}),
    ("sort:resolution", {"sort": "resolution", "order": "desc"}),
    ("sort:bitrate", {"sort": "bitrate", "order": "desc"}),
    ("sort:progress", {"sort": "progress", "order": "desc"}),
    ("sort:date_viewed", {"sort": "date_viewed", "order": "desc"}),
    ("sort:plays", {"sort": "plays", "order": "desc"}),
]

DEFAULT_FILTER_CASES = [
    ("filter:genre", {"genre": "Comedy", "sort": "title", "order": "asc"}),
    ("filter:year", {"year_min": "2020", "year_max": "2024", "sort": "year", "order": "desc"}),
    ("filter:content_rating", {"content_rating": "tv-pg", "sort": "title", "order": "asc"}),
    (
        "filter:resolution",
        group_rule("resolution", "is", "1080p") | {"sort": "title", "order": "asc"},
    ),
    ("filter:hdr", group_rule("hdr", "is", "true") | {"sort": "title", "order": "asc"}),
    (
        "filter:dolby_vision",
        group_rule("dolby_vision", "is", "true") | {"sort": "title", "order": "asc"},
    ),
    (
        "filter:bitrate",
        group_rule("bitrate", "gte", "8000000") | {"sort": "bitrate", "order": "desc"},
    ),
    (
        "filter:audio_language",
        group_rule("audio_language", "is", "en") | {"sort": "title", "order": "asc"},
    ),
    (
        "filter:subtitle_language",
        group_rule("subtitle_language", "is", "en") | {"sort": "title", "order": "asc"},
    ),
    (
        "filter:watched_true",
        group_rule("watched", "is", "true") | {"sort": "title", "order": "asc"},
    ),
    (
        "filter:in_progress_true",
        group_rule("in_progress", "is", "true") | {"sort": "progress", "order": "desc"},
    ),
    (
        "filter:last_watched_30d",
        group_rule("last_watched", "in_last", "30d") | {"sort": "date_viewed", "order": "desc"},
    ),
]


@dataclass(frozen=True)
class Config:
    base_url: str
    token: str
    profile_id: str
    library_id: str
    limit: int
    offset: int
    timeout: float


@dataclass(frozen=True)
class CaseResult:
    case: str
    include_total: bool
    status: int
    elapsed_ms: float
    total: int | None
    total_exact: bool | None
    has_more: bool | None
    item_count: int | None
    error: str


def getenv_required(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise SystemExit(f"{name} is required")
    return value


def validate_base_url(base_url: str) -> None:
    parsed = urllib.parse.urlparse(base_url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise ValueError("SILO_BASE_URL must be an http or https URL")


def build_url(config: Config, params: dict[str, str], include_total: bool) -> str:
    validate_base_url(config.base_url)
    query = {
        "source": "query",
        "type": "episode",
        "library_id": config.library_id,
        "limit": str(config.limit),
        "offset": str(config.offset),
        **params,
    }
    if not include_total:
        query["include_total"] = "false"
    api_url = urllib.parse.urljoin(config.base_url.rstrip("/") + "/", "api/v1/catalog")
    return api_url + "?" + urllib.parse.urlencode(query)


def fetch_case(config: Config, case: str, params: dict[str, str], include_total: bool) -> CaseResult:
    url = build_url(config, params, include_total)
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/json",
            "Authorization": f"Bearer {config.token}",
            "X-Profile-Id": config.profile_id,
        },
    )
    started = time.perf_counter()
    status = 0
    try:
        with urllib.request.urlopen(request, timeout=config.timeout) as response:
            status = response.status
            body = response.read()
            elapsed_ms = (time.perf_counter() - started) * 1000
            try:
                payload = json.loads(body)
                items = payload.get("items")
            except (json.JSONDecodeError, AttributeError, TypeError) as err:
                return CaseResult(
                    case,
                    include_total,
                    status,
                    elapsed_ms,
                    None,
                    None,
                    None,
                    None,
                    str(err),
                )
            return CaseResult(
                case=case,
                include_total=include_total,
                status=status,
                elapsed_ms=elapsed_ms,
                total=payload.get("total"),
                total_exact=payload.get("total_exact"),
                has_more=payload.get("has_more"),
                item_count=len(items) if isinstance(items, list) else None,
                error="",
            )
    except urllib.error.HTTPError as err:
        elapsed_ms = (time.perf_counter() - started) * 1000
        return CaseResult(case, include_total, err.code, elapsed_ms, None, None, None, None, err.reason)
    except Exception as err:  # noqa: BLE001 - benchmark output should capture failures.
        elapsed_ms = (time.perf_counter() - started) * 1000
        return CaseResult(case, include_total, 0, elapsed_ms, None, None, None, None, str(err))


def result_rows(results: Iterable[CaseResult]) -> list[dict[str, object]]:
    rows = []
    for result in results:
        rows.append(
            {
                "case": result.case,
                "include_total": str(result.include_total).lower(),
                "status": result.status,
                "elapsed_ms": f"{result.elapsed_ms:.1f}",
                "total": "" if result.total is None else result.total,
                "total_exact": "" if result.total_exact is None else str(result.total_exact).lower(),
                "has_more": "" if result.has_more is None else str(result.has_more).lower(),
                "item_count": "" if result.item_count is None else result.item_count,
                "error": result.error,
            }
        )
    return rows


def summarize(results: list[CaseResult]) -> None:
    successful = [result.elapsed_ms for result in results if result.status == 200]
    if not successful:
        return
    print(
        f"# successful={len(successful)} p50_ms={statistics.median(successful):.1f} "
        f"max_ms={max(successful):.1f}",
        file=sys.stderr,
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="Benchmark episode catalog sort/filter cases.")
    parser.add_argument("--limit", type=int, default=60)
    parser.add_argument("--offset", type=int, default=0)
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--concurrency", type=int, default=1)
    parser.add_argument("--include-exact", action="store_true", help="Also run cases with exact totals enabled.")
    args = parser.parse_args()

    config = Config(
        base_url=getenv_required("SILO_BASE_URL"),
        token=getenv_required("SILO_BEARER_TOKEN"),
        profile_id=getenv_required("SILO_PROFILE_ID"),
        library_id=os.environ.get("SILO_LIBRARY_ID", "2"),
        limit=args.limit,
        offset=args.offset,
        timeout=args.timeout,
    )
    try:
        validate_base_url(config.base_url)
    except ValueError as err:
        raise SystemExit(str(err)) from err

    cases = DEFAULT_SORT_CASES + DEFAULT_FILTER_CASES
    include_total_values = [False, True] if args.include_exact else [False]
    jobs = [
        (name, params, include_total)
        for _ in range(args.repeat)
        for include_total in include_total_values
        for name, params in cases
    ]

    results: list[CaseResult] = []
    with ThreadPoolExecutor(max_workers=max(1, args.concurrency)) as executor:
        futures = [executor.submit(fetch_case, config, *job) for job in jobs]
        for future in as_completed(futures):
            results.append(future.result())

    results.sort(key=lambda row: (row.case, row.include_total, row.elapsed_ms))
    writer = csv.DictWriter(
        sys.stdout,
        fieldnames=[
            "case",
            "include_total",
            "status",
            "elapsed_ms",
            "total",
            "total_exact",
            "has_more",
            "item_count",
            "error",
        ],
    )
    writer.writeheader()
    writer.writerows(result_rows(results))
    summarize(results)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
