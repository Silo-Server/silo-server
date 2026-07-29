#!/usr/bin/env python3
"""Fetch Discord thread/channel messages with a bot token from env or a local secret file."""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

from parse_discord_url import parse_discord_url  # noqa: E402


API_BASE = "https://discord.com/api/v10"
DEFAULT_TOKEN_ENV = "DISCORD_BOT_TOKEN"
DEFAULT_TOKEN_FILE = SCRIPT_DIR.parent / ".secrets" / "discord_bot_token"
USER_AGENT = "silo-discord-triage/1.0"


@dataclass
class FetchResult:
    source: str
    channel_id: str
    guild_id: str | None
    channel: dict[str, Any] | None
    message_count: int
    messages: list[dict[str, Any]]
    warnings: list[str]


class DiscordAPIError(RuntimeError):
    def __init__(self, status: int | None, message: str):
        self.status = status
        super().__init__(message)


def request_json(path: str, token: str, params: dict[str, Any] | None = None) -> Any:
    query = ""
    if params:
        query = "?" + urllib.parse.urlencode({key: value for key, value in params.items() if value is not None})
    request = urllib.request.Request(
        API_BASE + path + query,
        headers={
            "Authorization": f"Bot {token}",
            "User-Agent": USER_AGENT,
            "Accept": "application/json",
        },
    )

    while True:
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                if response.status == 204:
                    return None
                return json.loads(response.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            body = exc.read().decode("utf-8", errors="replace")
            retry_after = None
            try:
                retry_after = json.loads(body).get("retry_after")
            except json.JSONDecodeError:
                pass
            if exc.code == 429 and retry_after is not None:
                time.sleep(float(retry_after))
                continue
            raise DiscordAPIError(exc.code, format_http_error(exc.code, body)) from exc
        except urllib.error.URLError as exc:
            raise DiscordAPIError(None, f"Discord request failed: {exc.reason}") from exc


def format_http_error(status: int, body: str) -> str:
    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        payload = {}

    code = payload.get("code")
    message = payload.get("message") or "Discord API request failed"
    if status in {401, 403}:
        message += "; check the bot token, channel visibility, thread access, View Channel, and Read Message History permissions"
    return f"Discord API error {status}" + (f" ({code})" if code else "") + f": {message}"


def fetch_messages(channel_id: str, token: str, max_messages: int, anchor_message_id: str | None = None) -> list[dict[str, Any]]:
    remaining = max_messages
    messages: list[dict[str, Any]] = []
    before = None

    if anchor_message_id:
        # Anchor on the linked message so it is always included, with context on both sides.
        limit = min(100, remaining)
        page = request_json(f"/channels/{channel_id}/messages", token, {"limit": limit, "around": anchor_message_id})
        messages.extend(page or [])
        remaining -= len(messages)
        if not any(item.get("id") == anchor_message_id for item in messages):
            anchor = request_json(f"/channels/{channel_id}/messages/{anchor_message_id}", token)
            if isinstance(anchor, dict):
                messages.append(anchor)
                remaining -= 1
        if messages:
            before = min(messages, key=lambda item: int(item["id"]))["id"]

    while remaining > 0:
        limit = min(100, remaining)
        page = request_json(f"/channels/{channel_id}/messages", token, {"limit": limit, "before": before})
        if not page:
            break
        messages.extend(page)
        remaining -= len(page)
        before = page[-1]["id"]
        if len(page) < limit:
            break

    deduped = {item["id"]: item for item in messages}
    return sorted(deduped.values(), key=lambda item: int(item["id"]))


def pseudonymize_author(author_id: str, labels: dict[str, str]) -> str:
    if author_id not in labels:
        labels[author_id] = f"author_{len(labels) + 1}"
    return labels[author_id]


def attachment_summary(attachment: dict[str, Any], include_sensitive: bool) -> dict[str, Any]:
    summary = {
        "filename": attachment.get("filename"),
        "content_type": attachment.get("content_type"),
        "size": attachment.get("size"),
        "description": attachment.get("description"),
    }
    if include_sensitive:
        summary["id"] = attachment.get("id")
        summary["url"] = attachment.get("url")
        summary["proxy_url"] = attachment.get("proxy_url")
    return {key: value for key, value in summary.items() if value not in (None, "", [])}


def normalize_message(message: dict[str, Any], labels: dict[str, str], include_sensitive: bool, guild_id: str | None, anchor_message_id: str | None = None) -> dict[str, Any]:
    author = message.get("author") or {}
    author_id = author.get("id") or "unknown"
    normalized: dict[str, Any] = {
        "author": pseudonymize_author(author_id, labels),
        "timestamp": message.get("timestamp"),
        "edited_timestamp": message.get("edited_timestamp"),
        "content": message.get("content") or "",
        "attachments": [attachment_summary(item, include_sensitive) for item in message.get("attachments", [])],
        "embeds": message.get("embeds", []),
        "components": message.get("components", []),
    }
    if message.get("referenced_message"):
        normalized["references_message"] = True
    if anchor_message_id and message.get("id") == anchor_message_id:
        normalized["is_linked_message"] = True
    if include_sensitive:
        normalized["id"] = message.get("id")
        normalized["channel_id"] = message.get("channel_id")
        normalized["author_id"] = author_id
        normalized["author_username"] = author.get("username")
        normalized["author_global_name"] = author.get("global_name")
        normalized["jump_url"] = build_jump_url(message, guild_id)
    return {key: value for key, value in normalized.items() if value not in (None, "", [])}


def build_jump_url(message: dict[str, Any], guild_id: str | None) -> str | None:
    channel_id = message.get("channel_id")
    message_id = message.get("id")
    if channel_id and message_id:
        return f"https://discord.com/channels/{guild_id or '@me'}/{channel_id}/{message_id}"
    return None


def normalize_channel(channel: dict[str, Any], include_sensitive: bool) -> dict[str, Any]:
    keys = ["name", "type", "topic", "thread_metadata", "message_count", "member_count"]
    normalized = {key: channel.get(key) for key in keys if channel.get(key) not in (None, "", [])}
    if include_sensitive:
        normalized["id"] = channel.get("id")
        normalized["guild_id"] = channel.get("guild_id")
        normalized["parent_id"] = channel.get("parent_id")
    return normalized


def build_result(source: str, token: str, max_messages: int, include_sensitive: bool) -> FetchResult:
    parsed = parse_discord_url(source)
    if not parsed.valid or not parsed.channel_or_thread_id:
        raise SystemExit(f"Invalid Discord URL: {parsed.error}")

    channel = request_json(f"/channels/{parsed.channel_or_thread_id}", token)
    messages = fetch_messages(parsed.channel_or_thread_id, token, max_messages, parsed.message_id)
    labels: dict[str, str] = {}
    normalized_messages = [normalize_message(item, labels, include_sensitive, parsed.guild_id, parsed.message_id) for item in messages]
    warnings = build_warnings(messages, normalized_messages)

    return FetchResult(
        source="discord_url",
        channel_id=parsed.channel_or_thread_id if include_sensitive else "redacted",
        guild_id=parsed.guild_id if include_sensitive else None,
        channel=normalize_channel(channel, include_sensitive) if isinstance(channel, dict) else None,
        message_count=len(normalized_messages),
        messages=normalized_messages,
        warnings=warnings,
    )


def build_warnings(raw_messages: list[dict[str, Any]], normalized_messages: list[dict[str, Any]]) -> list[str]:
    warnings = []
    if raw_messages and not any(item.get("content") for item in raw_messages):
        warnings.append("All fetched messages have empty content; verify the bot has Message Content privileged intent access or that the thread contains only non-text content.")
    if raw_messages and any(item.get("attachments") for item in raw_messages):
        warnings.append("Attachment URLs are omitted by default; rerun with --include-sensitive only for private local diagnosis if direct attachment access is required.")
    if not normalized_messages:
        warnings.append("No messages returned; verify the bot can view the channel/thread and has Read Message History.")
    return warnings


def load_token(token_env: str, token_file: str) -> str | None:
    token = os.environ.get(token_env)
    if token:
        return token.strip()

    path = Path(token_file).expanduser()
    try:
        token = path.read_text(encoding="utf-8").strip()
    except FileNotFoundError:
        return None
    if not token:
        return None
    return token


def main() -> int:
    parser = argparse.ArgumentParser(description="Fetch Discord thread/channel messages using a scoped bot token.")
    parser.add_argument("url", help="Discord channel, thread, or message URL")
    parser.add_argument("--max-messages", type=int, default=300, help="Maximum messages to fetch, newest pages first then output oldest-first")
    parser.add_argument("--token-env", default=DEFAULT_TOKEN_ENV, help="Environment variable containing the bot token")
    parser.add_argument("--token-file", default=str(DEFAULT_TOKEN_FILE), help="Fallback file containing the bot token when the environment variable is unset")
    parser.add_argument("--include-sensitive", action="store_true", help="Include Discord IDs, usernames, message URLs, and attachment URLs")
    args = parser.parse_args()

    if args.max_messages < 1:
        raise SystemExit("--max-messages must be at least 1")

    token = load_token(args.token_env, args.token_file)
    if not token:
        raise SystemExit(f"No Discord bot token found; set {args.token_env} or create .secrets/discord_bot_token in the skill directory")

    try:
        result = build_result(args.url, token, args.max_messages, args.include_sensitive)
    except DiscordAPIError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    print(json.dumps(asdict(result), indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
