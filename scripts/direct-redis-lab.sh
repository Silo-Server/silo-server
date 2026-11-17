#!/usr/bin/env bash
# Run the live-Redis direct-connection store tests against a disposable Redis.
#
# Usage: SILO_DIRECT_TEST_REDIS_URL=redis://HOST:PORT/DB scripts/direct-redis-lab.sh DB
#
# DB is the one database index the tests may use. The script refuses to run
# unless the URL names that same index, and the tests themselves accept only
# index 15. The tests flush the database, so never point them at data you keep.
# The URL stays in the environment; the script never prints it.
set -euo pipefail

die() {
	printf 'direct-redis-lab: %s\n' "$1" >&2
	exit 2
}

[[ $# -eq 1 ]] || die "usage: SILO_DIRECT_TEST_REDIS_URL=redis://HOST:PORT/DB ${0##*/} DB"
db=$1
[[ $db =~ ^[0-9]+$ ]] || die "DB must be a decimal database index"

url=${SILO_DIRECT_TEST_REDIS_URL:-}
[[ -n $url ]] || die "SILO_DIRECT_TEST_REDIS_URL is not set"
case $url in
	redis://* | rediss://*) ;;
	*) die "SILO_DIRECT_TEST_REDIS_URL must be a redis:// or rediss:// URL" ;;
esac

rest=${url#*://}
rest=${rest%%\?*}
[[ $rest == */* ]] || die "SILO_DIRECT_TEST_REDIS_URL names no database index; refusing to use a default"
url_db=${rest#*/}
url_db=${url_db%%/*}
[[ $url_db == "$db" ]] || die "SILO_DIRECT_TEST_REDIS_URL names database index $url_db, not $db; refusing to run"

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
export SILO_DIRECT_TEST_REDIS_URL
# -p 1: the packages share database $db and each test flushes it, so they
# must not run at the same time.
exec go test -tags redislab -count=1 -p 1 -v \
	-run '^(TestRedisEnrollmentLimitAndConflict|TestRedisFlushForcesReenrollment|TestRedisLabConditionalStoreConformance|TestDirectSignalingRedisRestart|TestShutdownOrderRedis|TestShutdownReleasesLeaseWithOpenStream)$' \
	./internal/directconn/... ./internal/apiv2/
