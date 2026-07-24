#!/usr/bin/env bash
set -euo pipefail

usage() {
	printf 'usage: %s --path PATH [--json]\n' "${0##*/}" >&2
	printf '\n' >&2
	printf 'Compare a Silo jellycompat response with a reference Jellyfin response.\n' >&2
	printf '\n' >&2
	printf 'Required environment variables:\n' >&2
	printf '  SILO_BASE_URL  SILO_TOKEN  SILO_USER_ID\n' >&2
	printf '  JF_BASE_URL    JF_TOKEN    JF_USER_ID\n' >&2
}

endpoint_path=
json_output=0

while [[ "$#" -gt 0 ]]; do
	case "$1" in
	--path)
		if [[ "$#" -lt 2 ]]; then
			printf '%s\n' "error: --path requires a value" >&2
			usage
			exit 2
		fi
		endpoint_path=$2
		shift 2
		;;
	--path=*)
		endpoint_path=${1#*=}
		shift
		;;
	--json)
		json_output=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		printf 'error: unknown argument: %s\n' "$1" >&2
		usage
		exit 2
		;;
	esac
done

if [[ -z "$endpoint_path" ]]; then
	printf '%s\n' "error: --path is required" >&2
	usage
	exit 2
fi

if [[ "$endpoint_path" != /* ]]; then
	printf '%s\n' "error: --path must start with /" >&2
	exit 2
fi

require_env() {
	local name=$1

	if [[ -z "${!name:-}" ]]; then
		printf 'error: required environment variable %s is unset or empty\n' "$name" >&2
		exit 1
	fi
}

require_env SILO_BASE_URL
require_env SILO_TOKEN
require_env SILO_USER_ID
require_env JF_BASE_URL
require_env JF_TOKEN
require_env JF_USER_ID

temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

FETCH_CURL_EXIT=0
FETCH_HTTP_STATUS=000
FETCH_TIME_SECONDS=

fetch_side() {
	local base_url=$1
	local token=$2
	local request_path=$3
	local body_file=$4
	local error_file=$5
	local metrics

	FETCH_CURL_EXIT=0
	FETCH_HTTP_STATUS=000
	FETCH_TIME_SECONDS=

	metrics=$(
		curl \
			--silent \
			--show-error \
			--output "$body_file" \
			--write-out $'%{http_code}\t%{time_total}' \
			--header "X-Emby-Token: ${token}" \
			"${base_url%/}${request_path}" \
			2>"$error_file"
	) || FETCH_CURL_EXIT=$?

	IFS=$'\t' read -r FETCH_HTTP_STATUS FETCH_TIME_SECONDS <<<"$metrics" || true
}

silo_path=${endpoint_path//\{userId\}/$SILO_USER_ID}
jf_path=${endpoint_path//\{userId\}/$JF_USER_ID}

fetch_side "$SILO_BASE_URL" "$SILO_TOKEN" "$silo_path" "$temp_dir/silo.body" "$temp_dir/silo.error"
silo_curl_exit=$FETCH_CURL_EXIT
silo_http_status=$FETCH_HTTP_STATUS
silo_time_seconds=$FETCH_TIME_SECONDS

fetch_side "$JF_BASE_URL" "$JF_TOKEN" "$jf_path" "$temp_dir/jellyfin.body" "$temp_dir/jellyfin.error"
jf_curl_exit=$FETCH_CURL_EXIT
jf_http_status=$FETCH_HTTP_STATUS
jf_time_seconds=$FETCH_TIME_SECONDS

python3 - \
	"$json_output" \
	"$endpoint_path" \
	"$temp_dir/silo.body" \
	"$temp_dir/silo.error" \
	"$silo_curl_exit" \
	"$silo_http_status" \
	"$silo_time_seconds" \
	"$silo_path" \
	"$temp_dir/jellyfin.body" \
	"$temp_dir/jellyfin.error" \
	"$jf_curl_exit" \
	"$jf_http_status" \
	"$jf_time_seconds" \
	"$jf_path" <<'PY'
import json
import os
import sys


def parse_status(raw_status):
    try:
        return int(raw_status)
    except ValueError:
        return 0


def parse_latency_ms(raw_seconds):
    try:
        return round(float(raw_seconds) * 1000, 3)
    except ValueError:
        return None


def read_text(path):
    try:
        with open(path, encoding="utf-8", errors="replace") as handle:
            return handle.read().strip()
    except FileNotFoundError:
        return ""


def analyze_side(body_path, error_path, curl_exit, raw_status, raw_latency, request_path):
    status = parse_status(raw_status)
    body_bytes = os.path.getsize(body_path) if os.path.exists(body_path) else 0
    request_error = read_text(error_path) if curl_exit != 0 else None
    response_error = None
    total_record_count = None
    items_length = None
    shape = None
    item_keys = set()

    if curl_exit == 0 and 200 <= status < 300:
        try:
            with open(body_path, encoding="utf-8") as handle:
                payload = json.load(handle)

            # Three response shapes are all legitimate here:
            #   envelope — {Items, TotalRecordCount, ...} from list endpoints
            #   array    — bare BaseItemDto[] from LocalTrailers / SpecialFeatures
            #   object   — the item itself, from detail endpoints like /Items/{id}
            # Treating anything but the envelope as an error, or as zero items,
            # would report a false clean on exactly the endpoints most likely to
            # have drifted.
            if isinstance(payload, list):
                shape = "array"
                items = payload
            elif isinstance(payload, dict):
                total_record_count = payload.get("TotalRecordCount")
                if "Items" in payload:
                    shape = "envelope"
                    items = payload["Items"]
                    if not isinstance(items, list):
                        raise ValueError("Items is not an array")
                else:
                    shape = "object"
                    items = [payload]
            else:
                raise ValueError("top-level JSON value is neither an object nor an array")

            items_length = len(items)
            for index, item in enumerate(items):
                if not isinstance(item, dict):
                    raise ValueError(f"item[{index}] is not an object")
                item_keys.update(item)
        except (OSError, UnicodeError, json.JSONDecodeError, ValueError) as error:
            response_error = str(error)

    bytes_per_item = None
    if items_length:
        bytes_per_item = round(body_bytes / items_length, 2)

    return {
        "requestPath": request_path,
        "curlExitCode": curl_exit,
        "httpStatus": status,
        "latencyMs": parse_latency_ms(raw_latency),
        "bodyBytes": body_bytes,
        "totalRecordCount": total_record_count,
        "responseShape": shape,
        "itemsLength": items_length,
        "itemKeys": sorted(item_keys),
        "bytesPerItem": bytes_per_item,
        "requestError": request_error,
        "responseError": response_error,
    }


def is_success(side):
    return (
        side["curlExitCode"] == 0
        and 200 <= side["httpStatus"] < 300
        and side["responseError"] is None
    )


def display_value(value):
    if value is None:
        return "not present"
    return str(value)


def display_keys(keys):
    return ", ".join(keys) if keys else "(none)"


def print_side(name, side):
    latency = side["latencyMs"]
    latency_display = f"{latency:.3f} ms" if latency is not None else "not available"
    bytes_per_item = side["bytesPerItem"]
    bytes_per_item_display = (
        f"{bytes_per_item:.2f}" if bytes_per_item is not None else "not available"
    )

    print(f"{name}:")
    print(f"  Request path: {side['requestPath']}")
    print(f"  HTTP status: {side['httpStatus']}")
    print(f"  Latency: {latency_display}")
    print(f"  Response body: {side['bodyBytes']} bytes")
    print(f"  Response shape: {display_value(side['responseShape'])}")
    print(f"  TotalRecordCount: {display_value(side['totalRecordCount'])}")
    print(f"  Items length: {display_value(side['itemsLength'])}")
    print(f"  Per-item keys ({len(side['itemKeys'])}): {display_keys(side['itemKeys'])}")
    print(f"  Approx. bytes per item: {bytes_per_item_display}")
    if side["requestError"]:
        print(f"  Request error: {side['requestError']}")
    if side["responseError"]:
        print(f"  Response error: {side['responseError']}")


json_output = sys.argv[1] == "1"
endpoint_path = sys.argv[2]
silo = analyze_side(
    sys.argv[3],
    sys.argv[4],
    int(sys.argv[5]),
    sys.argv[6],
    sys.argv[7],
    sys.argv[8],
)
jellyfin = analyze_side(
    sys.argv[9],
    sys.argv[10],
    int(sys.argv[11]),
    sys.argv[12],
    sys.argv[13],
    sys.argv[14],
)

silo_keys = set(silo["itemKeys"])
jellyfin_keys = set(jellyfin["itemKeys"])
silo_bytes_per_item = silo["bytesPerItem"]
jellyfin_bytes_per_item = jellyfin["bytesPerItem"]
bytes_warning = (
    silo_bytes_per_item is not None
    and jellyfin_bytes_per_item is not None
    and silo_bytes_per_item > jellyfin_bytes_per_item * 2
)

# A shape difference is itself a compat bug — an envelope where Jellyfin sends a
# bare array breaks clients regardless of whether the per-item keys agree.
shape_mismatch = (
    is_success(silo)
    and is_success(jellyfin)
    and silo["responseShape"] != jellyfin["responseShape"]
)

comparison = {
    "path": endpoint_path,
    "ok": is_success(silo) and is_success(jellyfin),
    "silo": silo,
    "jellyfin": jellyfin,
    "diff": {
        "missingFromSilo": sorted(jellyfin_keys - silo_keys),
        "siloOnly": sorted(silo_keys - jellyfin_keys),
        "shared": sorted(silo_keys & jellyfin_keys),
        "responseShapeMismatch": shape_mismatch,
        "siloBytesPerItemExceedsJellyfinByMoreThan2x": bytes_warning,
    },
}

if json_output:
    print(json.dumps(comparison, separators=(",", ":"), sort_keys=True))
else:
    print_side("Silo", silo)
    print()
    print_side("Jellyfin", jellyfin)
    print()
    print("Diff:")
    print(
        "  Jellyfin keys missing from Silo "
        f"({len(comparison['diff']['missingFromSilo'])}): "
        f"{display_keys(comparison['diff']['missingFromSilo'])}"
    )
    print(
        f"  Silo-only keys ({len(comparison['diff']['siloOnly'])}): "
        f"{display_keys(comparison['diff']['siloOnly'])}"
    )
    print(
        f"  Keys in both ({len(comparison['diff']['shared'])}): "
        f"{display_keys(comparison['diff']['shared'])}"
    )
    if shape_mismatch:
        print(
            f"  WARNING: response shape differs — Silo returned "
            f"{silo['responseShape']}, Jellyfin returned {jellyfin['responseShape']}."
        )
    if bytes_warning:
        print(
            "  WARNING: Silo's approximate bytes per item exceeds "
            "Jellyfin's by more than 2x."
        )

sys.exit(0 if comparison["ok"] else 1)
PY
