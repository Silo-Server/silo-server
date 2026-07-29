#!/usr/bin/env python3
"""Parse Discord channel, thread, and message URLs without network access."""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import asdict, dataclass
from urllib.parse import urlparse


DISCORD_HOSTS = {"discord.com", "www.discord.com", "discordapp.com", "www.discordapp.com"}
SNOWFLAKE_RE = re.compile(r"^\d{15,25}$")


@dataclass
class ParsedDiscordURL:
    input: str
    valid: bool
    host: str | None = None
    guild_id: str | None = None
    channel_or_thread_id: str | None = None
    message_id: str | None = None
    is_dm: bool = False
    kind: str | None = None
    error: str | None = None


def parse_discord_url(raw_url: str) -> ParsedDiscordURL:
    parsed = urlparse(raw_url.strip())
    host = parsed.netloc.lower()
    if host not in DISCORD_HOSTS:
        return ParsedDiscordURL(input=raw_url, valid=False, host=host or None, error="not a Discord URL")

    parts = [part for part in parsed.path.split("/") if part]
    if len(parts) < 3 or parts[0] != "channels":
        return ParsedDiscordURL(input=raw_url, valid=False, host=host, error="expected /channels/<guild>/<channel-or-thread>/<message?>")

    guild_id, channel_id = parts[1], parts[2]
    message_id = parts[3] if len(parts) > 3 else None
    is_dm = guild_id == "@me"

    bad_fields = []
    if not is_dm and not SNOWFLAKE_RE.match(guild_id):
        bad_fields.append("guild_id")
    if not SNOWFLAKE_RE.match(channel_id):
        bad_fields.append("channel_or_thread_id")
    if message_id is not None and not SNOWFLAKE_RE.match(message_id):
        bad_fields.append("message_id")
    if bad_fields:
        return ParsedDiscordURL(
            input=raw_url,
            valid=False,
            host=host,
            guild_id=None if is_dm else guild_id,
            channel_or_thread_id=channel_id,
            message_id=message_id,
            is_dm=is_dm,
            error="invalid Discord snowflake field(s): " + ", ".join(bad_fields),
        )

    if message_id:
        kind = "message_link"
    elif is_dm:
        kind = "dm_channel_link"
    else:
        kind = "channel_or_thread_link"

    return ParsedDiscordURL(
        input=raw_url,
        valid=True,
        host=host,
        guild_id=None if is_dm else guild_id,
        channel_or_thread_id=channel_id,
        message_id=message_id,
        is_dm=is_dm,
        kind=kind,
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="Parse a Discord URL into channel/thread/message identifiers.")
    parser.add_argument("url", help="Discord URL to parse")
    parser.add_argument("--text", action="store_true", help="Print a short text summary instead of JSON")
    args = parser.parse_args()

    result = parse_discord_url(args.url)
    if args.text:
        if not result.valid:
            print(f"invalid: {result.error}")
        else:
            message_part = f", message={result.message_id}" if result.message_id else ""
            guild_part = "dm" if result.is_dm else f"guild={result.guild_id}"
            print(f"{result.kind}: {guild_part}, channel_or_thread={result.channel_or_thread_id}{message_part}")
    else:
        print(json.dumps(asdict(result), indent=2, sort_keys=True))
    return 0 if result.valid else 2


if __name__ == "__main__":
    sys.exit(main())
