#!/usr/bin/env python3
"""
One-shot audiobook deduplication.

The audiobook library frequently contains the same book stored in
two folders — one folder uses the short title and the other appends the
subtitle. Both get scanned as separate media_items rows even though
they're the same recording. Detection rules:

  * Same author (item_people kind=7)
  * Same narrator (item_people kind=8)
  * Same release year
  * Total duration within 0.5% (or 10 seconds, whichever is larger)
  * Title compatible — equal core (text before the first ":") OR one
    title is a prefix of the other

Groups are computed by union-find across all such pairs. Each group's
canonical row is the one with the shortest title (preferring less-
subtitle-polluted variants). Files and progress on non-canonical rows
are repointed to the canonical row; non-canonical media_items rows are
then deleted (with cascading cleanup of item_people, audiobook_series,
media_item_libraries, etc.).

Usage:
    python3 scripts/dedup_audiobooks.py            # dry-run (default)
    python3 scripts/dedup_audiobooks.py --apply    # actually merge
    python3 scripts/dedup_audiobooks.py --apply --limit 1   # merge one group, for spot-checking
"""

from __future__ import annotations

import argparse
import os
import sys
from collections import defaultdict

import psycopg2


def db_connect():
    return psycopg2.connect(
        host=os.environ.get("PGHOST", "localhost"),
        port=int(os.environ.get("PGPORT", 5432)),
        user=os.environ.get("PGUSER", "silo"),
        password=os.environ.get("PGPASSWORD", "silo"),
        dbname=os.environ.get("PGDATABASE", "silo"),
    )


def find_duplicate_pairs(cur):
    # Title match rule: equal, OR one is an exact prefix of the other
    # followed by ":" (the canonical "Title" vs "Title: Subtitle" case).
    # Anything looser (e.g. matching just before the first colon) wrongly
    # merges series — e.g. eight "Breathe - Overcoming Anxiety: <topic>"
    # books each on a different topic.
    cur.execute(
        r"""
        WITH meta AS (
          SELECT mi.content_id,
                 mi.title,
                 mi.year,
                 (SELECT ip.person_id FROM item_people ip WHERE ip.content_id=mi.content_id AND ip.kind=7 LIMIT 1) AS author_id,
                 (SELECT ip.person_id FROM item_people ip WHERE ip.content_id=mi.content_id AND ip.kind=8 LIMIT 1) AS narrator_id,
                 (SELECT SUM(mf.duration) FROM media_files mf WHERE mf.content_id=mi.content_id) AS dur
          FROM media_items mi WHERE mi.type='audiobook'
        )
        SELECT a.content_id, b.content_id
        FROM meta a JOIN meta b
          ON a.author_id IS NOT NULL AND a.author_id = b.author_id
          AND a.narrator_id IS NOT NULL AND a.narrator_id = b.narrator_id
          AND a.year = b.year
          AND a.content_id < b.content_id
          AND a.dur IS NOT NULL AND b.dur IS NOT NULL
          AND ABS(a.dur - b.dur) <= GREATEST(10, (a.dur * 0.005)::int)
          AND (
                LOWER(a.title) = LOWER(b.title)
             OR (LENGTH(b.title) > LENGTH(a.title)
                 AND LOWER(b.title) LIKE LOWER(a.title) || ':%')
             OR (LENGTH(a.title) > LENGTH(b.title)
                 AND LOWER(a.title) LIKE LOWER(b.title) || ':%')
          )
        """
    )
    return cur.fetchall()


def group_by_union_find(pairs):
    parent: dict[str, str] = {}

    def find(x: str) -> str:
        while parent.get(x, x) != x:
            x = parent[x]
        return x

    def union(a: str, b: str) -> None:
        ra, rb = find(a), find(b)
        if ra != rb:
            parent[ra] = rb

    for a, b in pairs:
        parent.setdefault(a, a)
        parent.setdefault(b, b)
        union(a, b)

    groups: dict[str, list[str]] = defaultdict(list)
    for node in parent:
        groups[find(node)].append(node)
    return list(groups.values())


def fetch_group_titles(cur, ids):
    cur.execute(
        "SELECT content_id, title, LENGTH(title) FROM media_items WHERE content_id = ANY(%s)",
        (ids,),
    )
    return cur.fetchall()


def pick_canonical(rows):
    # Shortest title wins; tiebreak on content_id (oldest = stable).
    rows_sorted = sorted(rows, key=lambda r: (r[2], r[0]))
    return rows_sorted[0][0]


def merge_group(cur, canonical: str, others: list[str], dry_run: bool) -> dict[str, int]:
    """Merge the `others` rows into the `canonical` row. Returns counts."""
    counts = {"media_files": 0, "watch_progress": 0, "item_people_deleted": 0, "series_deleted": 0}

    if dry_run:
        cur.execute("SELECT count(*) FROM media_files WHERE content_id = ANY(%s)", (others,))
        counts["media_files"] = cur.fetchone()[0]
        cur.execute(
            "SELECT count(*) FROM user_watch_progress WHERE media_item_id = ANY(%s)",
            (others,),
        )
        counts["watch_progress"] = cur.fetchone()[0]
        return counts

    # Preserve a dropped row's title in the canonical's original_title so the
    # subtitle info isn't lost when the longer-title row is deleted. Pick the
    # longest of the dropped titles as the "fuller" published form.
    cur.execute(
        """
        UPDATE media_items SET original_title = sub.fuller_title
        FROM (
            SELECT title AS fuller_title
            FROM media_items WHERE content_id = ANY(%s)
            ORDER BY LENGTH(title) DESC LIMIT 1
        ) sub
        WHERE content_id = %s
          AND COALESCE(original_title, '') = ''
        """,
        (others, canonical),
    )

    # Repoint media_files. The (content_id, file_path) PK should be unique
    # so we don't expect collisions; if we hit one, skip that row and
    # leave it for manual review.
    cur.execute(
        "UPDATE media_files SET content_id = %s WHERE content_id = ANY(%s)",
        (canonical, others),
    )
    counts["media_files"] = cur.rowcount

    # Repoint watch progress. ON CONFLICT in case a user already has
    # progress on both rows — keep the one with the higher position.
    cur.execute(
        """
        INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at)
        SELECT user_id, profile_id, %s, position_seconds, duration_seconds, completed, updated_at
        FROM user_watch_progress WHERE media_item_id = ANY(%s)
        ON CONFLICT (user_id, profile_id, media_item_id) DO UPDATE
        SET position_seconds = GREATEST(user_watch_progress.position_seconds, EXCLUDED.position_seconds),
            completed = user_watch_progress.completed OR EXCLUDED.completed,
            updated_at = NOW()
        """,
        (canonical, others),
    )
    counts["watch_progress"] = cur.rowcount

    # Delete the non-canonical media_items rows. Cascading FKs clean up
    # item_people, audiobook_series, media_item_libraries, and the
    # now-orphaned user_watch_progress entries on the other rows.
    cur.execute("DELETE FROM media_items WHERE content_id = ANY(%s)", (others,))
    return counts


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--apply", action="store_true", help="actually perform the merge")
    ap.add_argument("--limit", type=int, default=0, help="only merge the first N groups")
    args = ap.parse_args()

    conn = db_connect()
    conn.autocommit = False
    cur = conn.cursor()

    pairs = find_duplicate_pairs(cur)
    groups = group_by_union_find(pairs)
    print(f"detected {len(pairs)} duplicate pairs in {len(groups)} groups")
    if not groups:
        return

    if args.limit > 0:
        groups = groups[: args.limit]
        print(f"limiting to {len(groups)} groups")

    total_drop = 0
    total_files = 0
    total_progress = 0
    for group in groups:
        rows = fetch_group_titles(cur, group)
        canonical = pick_canonical(rows)
        others = [r[0] for r in rows if r[0] != canonical]
        titles_by_id = {r[0]: r[1] for r in rows}
        counts = merge_group(cur, canonical, others, dry_run=not args.apply)
        total_drop += len(others)
        total_files += counts["media_files"]
        total_progress += counts["watch_progress"]
        action = "MERGE" if args.apply else "DRY"
        print(f"{action} group ({len(rows)} rows): canonical={titles_by_id[canonical]!r}")
        for o in others:
            print(f"    └─ drop {titles_by_id[o]!r}")
        print(f"    files repointed: {counts['media_files']}, progress rows: {counts['watch_progress']}")

    if args.apply:
        conn.commit()
        print(f"\n✓ committed: dropped {total_drop} media_items, repointed {total_files} files, {total_progress} progress rows")
    else:
        conn.rollback()
        print(f"\n— DRY RUN — would drop {total_drop} media_items, repoint {total_files} files, {total_progress} progress rows")
        print("    re-run with --apply to actually merge")


if __name__ == "__main__":
    main()
