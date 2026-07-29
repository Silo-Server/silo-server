#!/usr/bin/env python3
"""Discover new active Discord threads for Silo intake automation."""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

from fetch_discord_thread import DEFAULT_TOKEN_ENV, DEFAULT_TOKEN_FILE, DiscordAPIError, load_token, request_json  # noqa: E402


DISCORD_EPOCH_MS = 1420070400000
DEFAULT_CONFIG_FILE = SCRIPT_DIR.parent / ".secrets" / "discord_monitor.json"
DEFAULT_STATE_FILE = SCRIPT_DIR.parent / ".state" / "discord_seen_threads.json"


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def snowflake_timestamp(snowflake: str) -> str | None:
    try:
        timestamp_ms = (int(snowflake) >> 22) + DISCORD_EPOCH_MS
    except ValueError:
        return None
    return datetime.fromtimestamp(timestamp_ms / 1000, tz=timezone.utc).isoformat().replace("+00:00", "Z")


def read_json(path: Path, default: dict[str, Any]) -> dict[str, Any]:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return default


def write_json(path: Path, data: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp_path = path.with_suffix(path.suffix + ".tmp")
    tmp_path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.chmod(tmp_path, 0o600)
    tmp_path.replace(path)
    os.chmod(path, 0o600)


def load_config(path: Path) -> dict[str, Any]:
    config = read_json(path, {})
    guild_ids = config.get("guild_ids")
    if not isinstance(guild_ids, list) or not guild_ids:
        raise SystemExit(f"Missing guild_ids in {path}")
    return config


def thread_url(guild_id: str, thread_id: str) -> str:
    return f"https://discord.com/channels/{guild_id}/{thread_id}"


def normalize_thread(guild_id: str, thread: dict[str, Any]) -> dict[str, Any]:
    thread_id = thread["id"]
    return {
        "created_at": snowflake_timestamp(thread_id),
        "last_message_id": thread.get("last_message_id"),
        "message_count": thread.get("message_count"),
        "name": thread.get("name") or "untitled-discord-thread",
        "parent_channel_id": thread.get("parent_id"),
        "thread_id": thread_id,
        "type": thread.get("type"),
        "url": thread_url(guild_id, thread_id),
    }


def iter_active_threads(token: str, config: dict[str, Any]) -> list[dict[str, Any]]:
    parent_filter = set(config.get("parent_channel_ids") or [])
    ignored = set(config.get("ignore_thread_ids") or [])
    discovered: list[dict[str, Any]] = []
    for guild_id in config["guild_ids"]:
        payload = request_json(f"/guilds/{guild_id}/threads/active", token)
        for thread in payload.get("threads", []):
            thread_id = thread.get("id")
            if not thread_id or thread_id in ignored:
                continue
            parent_id = thread.get("parent_id")
            if parent_filter and parent_id not in parent_filter:
                continue
            discovered.append(normalize_thread(guild_id, thread))
    discovered.sort(key=lambda item: item.get("thread_id") or "", reverse=True)
    return discovered


def mark_seen(state: dict[str, Any], threads: list[dict[str, Any]]) -> None:
    seen = state.setdefault("seen_threads", {})
    timestamp = now_iso()
    for thread in threads:
        thread_id = thread["thread_id"]
        record = seen.setdefault(thread_id, {})
        record.update(
            {
                "first_seen_at": record.get("first_seen_at") or timestamp,
                "last_seen_at": timestamp,
                "name": thread.get("name"),
                "url": thread.get("url"),
            }
        )


def mark_thread_ids(state: dict[str, Any], thread_ids: list[str]) -> None:
    seen = state.setdefault("seen_threads", {})
    timestamp = now_iso()
    for thread_id in thread_ids:
        record = seen.setdefault(thread_id, {})
        record.update(
            {
                "first_seen_at": record.get("first_seen_at") or timestamp,
                "last_seen_at": timestamp,
            }
        )


def main() -> int:
    parser = argparse.ArgumentParser(description="Discover new active Discord threads visible to the Silo intake bot.")
    parser.add_argument("--config-file", default=str(DEFAULT_CONFIG_FILE), help="Discord monitor config JSON")
    parser.add_argument("--state-file", default=str(DEFAULT_STATE_FILE), help="Seen-thread state JSON")
    parser.add_argument("--token-env", default=DEFAULT_TOKEN_ENV, help="Environment variable containing the bot token")
    parser.add_argument("--token-file", default=str(DEFAULT_TOKEN_FILE), help="Fallback file containing the bot token")
    parser.add_argument("--max-new", type=int, default=20, help="Maximum new threads to emit")
    parser.add_argument("--init", action="store_true", help="Mark all currently visible active threads as seen")
    parser.add_argument("--include-seen", action="store_true", help="Emit seen threads too")
    parser.add_argument("--mark-seen", action="store_true", help="Mark emitted new threads as seen")
    parser.add_argument("--mark-thread-id", action="append", default=[], help="Mark one specific Discord thread ID as seen without discovery")
    args = parser.parse_args()

    state_path = Path(args.state_file).expanduser()
    state = read_json(state_path, {"seen_threads": {}})
    if args.mark_thread_id:
        mark_thread_ids(state, args.mark_thread_id)
        write_json(state_path, state)
        print(json.dumps({"marked_seen": len(args.mark_thread_id)}, indent=2, sort_keys=True))
        return 0

    token = load_token(args.token_env, args.token_file)
    if not token:
        raise SystemExit(f"No Discord bot token found; set {args.token_env} or create .secrets/discord_bot_token in the skill directory")

    config_path = Path(args.config_file).expanduser()
    config = load_config(config_path)

    try:
        active_threads = iter_active_threads(token, config)
    except DiscordAPIError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    seen = state.setdefault("seen_threads", {})
    if args.init:
        mark_seen(state, active_threads)
        write_json(state_path, state)
        print(json.dumps({"initialized": True, "marked_seen": len(active_threads)}, indent=2, sort_keys=True))
        return 0

    if args.include_seen:
        output_threads = active_threads
    else:
        output_threads = [thread for thread in active_threads if thread["thread_id"] not in seen]
    output_threads = output_threads[: args.max_new]

    if args.mark_seen:
        mark_seen(state, output_threads)
        write_json(state_path, state)

    print(
        json.dumps(
            {
                "new_thread_count": len(output_threads),
                "threads": output_threads,
            },
            indent=2,
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
