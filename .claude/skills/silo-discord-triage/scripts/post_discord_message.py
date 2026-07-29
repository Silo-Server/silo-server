#!/usr/bin/env python3
"""Validate or post one explicitly approved Discord message with the scoped bot token."""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))

from fetch_discord_thread import (  # noqa: E402
    API_BASE,
    DEFAULT_TOKEN_ENV,
    DEFAULT_TOKEN_FILE,
    USER_AGENT,
    DiscordAPIError,
    load_token,
    request_json,
)
from parse_discord_url import parse_discord_url  # noqa: E402


def post_json(path: str, token: str, payload: dict[str, Any]) -> Any:
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    while True:
        request = urllib.request.Request(
            API_BASE + path,
            data=body,
            method="POST",
            headers={
                "Authorization": f"Bot {token}",
                "User-Agent": USER_AGENT,
                "Accept": "application/json",
                "Content-Type": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                return json.loads(response.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            response_body = exc.read().decode("utf-8", errors="replace")
            retry_after = None
            try:
                retry_after = json.loads(response_body).get("retry_after")
            except json.JSONDecodeError:
                pass
            if exc.code == 429 and retry_after is not None:
                time.sleep(float(retry_after))
                continue
            raise DiscordAPIError(exc.code, format_post_error(exc.code, response_body)) from exc
        except urllib.error.URLError as exc:
            raise DiscordAPIError(None, f"Discord post failed: {exc.reason}") from exc


def format_post_error(status: int, body: str) -> str:
    try:
        payload = json.loads(body)
    except json.JSONDecodeError:
        payload = {}
    code = payload.get("code")
    message = payload.get("message") or "Discord API request failed"
    if status == 401:
        message += "; check the bot token"
    elif status == 403:
        message += (
            "; check View Channel and Send Messages, plus Send Messages in Threads "
            "and thread membership/visibility when posting to a thread"
        )
    return f"Discord API error {status}" + (f" ({code})" if code else "") + f": {message}"


def read_content(raw_content: str | None, content_file: str | None) -> str:
    if raw_content is not None:
        content = raw_content
    elif content_file == "-":
        content = sys.stdin.read()
    elif content_file:
        content = Path(content_file).expanduser().read_text(encoding="utf-8")
    else:
        raise SystemExit("Provide exactly one of --content or --content-file")

    content = content.rstrip("\r\n")
    if not content.strip():
        raise SystemExit("Discord message content must not be empty")
    if len(content) > 2000:
        raise SystemExit(f"Discord message content is {len(content)} characters; maximum is 2000")
    return content


def content_fingerprint(content: str) -> str:
    return hashlib.sha256(content.encode("utf-8")).hexdigest()[:16]


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Validate or post one explicitly approved Discord message using the scoped bot token."
    )
    parser.add_argument("url", help="Discord channel, thread, or message URL")
    content_group = parser.add_mutually_exclusive_group(required=True)
    content_group.add_argument("--content", help="Message content")
    content_group.add_argument("--content-file", help="UTF-8 message file, or - for stdin")
    parser.add_argument("--confirm", action="store_true", help="Actually post after the exact content is approved")
    parser.add_argument("--token-env", default=DEFAULT_TOKEN_ENV, help="Environment variable containing the bot token")
    parser.add_argument("--token-file", default=str(DEFAULT_TOKEN_FILE), help="Fallback local token file")
    args = parser.parse_args()

    parsed = parse_discord_url(args.url)
    if not parsed.valid or not parsed.channel_or_thread_id:
        raise SystemExit(f"Invalid Discord URL: {parsed.error}")
    content = read_content(args.content, args.content_file)
    token = load_token(args.token_env, args.token_file)
    if not token:
        raise SystemExit(
            f"No Discord bot token found; set {args.token_env} or create "
            ".secrets/discord_bot_token in the skill directory"
        )

    try:
        channel = request_json(f"/channels/{parsed.channel_or_thread_id}", token)
        result: dict[str, Any] = {
            "valid": True,
            "posted": False,
            "channel_type": channel.get("type") if isinstance(channel, dict) else None,
            "is_thread": bool(isinstance(channel, dict) and channel.get("type") in {10, 11, 12}),
            "content_length": len(content),
            "content_sha256": content_fingerprint(content),
            "mentions_disabled": True,
        }
        if not args.confirm:
            print(json.dumps(result, indent=2, sort_keys=True))
            return 0

        response = post_json(
            f"/channels/{parsed.channel_or_thread_id}/messages",
            token,
            {
                "content": content,
                "allowed_mentions": {
                    "parse": [],
                    "replied_user": False,
                },
            },
        )
        result.update(
            {
                "posted": True,
                "verified": isinstance(response, dict) and response.get("content") == content,
                "timestamp": response.get("timestamp") if isinstance(response, dict) else None,
            }
        )
        if not result["verified"]:
            print(json.dumps(result, indent=2, sort_keys=True), file=sys.stderr)
            return 1
        print(json.dumps(result, indent=2, sort_keys=True))
        return 0
    except DiscordAPIError as exc:
        print(str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
