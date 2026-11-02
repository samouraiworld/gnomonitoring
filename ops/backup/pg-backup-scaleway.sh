#!/usr/bin/env bash
# Daily encrypted backup of the gnomonitoring Postgres database to Scaleway
# Object Storage (any S3-compatible endpoint works).
#
#   docker exec <container> pg_dump -Fc | age -R <recipients> > <work dir>/<name>
#   aws s3 cp <work dir>/<name> s3://<bucket>/<prefix><name>
#   aws s3api head-object: the stored size must equal the local size
#
# <name> is gnomonitoring-<UTC timestamp>.dump.age. Only age ciphertext is
# ever written to disk: the plaintext dump exists only in the pipe, and the
# private key able to decrypt it is never on this host.
#
# Usage:
#   pg-backup-scaleway.sh [--config FILE] [--check]
#
#   --config FILE  env file to read (default /etc/gnomonitoring/pg-backup.env)
#   --check        run every pre-flight check (config, tools, container,
#                  database, bucket, free space) and stop before the dump
#
# On any failure the script exits non-zero and, once DISCORD_WEBHOOK_URL has
# been read from a config file owned by root, posts a message to it. A run that
# never starts (cron down, missing script or config) cannot notify: use
# HEARTBEAT_URL for that. Install, restore and configuration:
# ops/backup/README.md.
set -euo pipefail
umask 077

CONFIG_FILE=/etc/gnomonitoring/pg-backup.env
CHECK_ONLY=false

# Defaults. Every key can be set in the config file (see pg-backup.env.example).
PG_CONTAINER=gnomonitoring-postgres
PG_USER=gnomonitoring
PG_DB=gnomonitoring
S3_BUCKET=gnomonitoring
S3_PREFIX=postgres/
S3_ENDPOINT=https://s3.fr-par.scw.cloud
S3_REGION=fr-par
AWS_PROFILE=
AGE_RECIPIENTS_FILE=/etc/gnomonitoring/pg-backup.recipients
DISCORD_WEBHOOK_URL=
NOTIFY_ON_SUCCESS=false
HEARTBEAT_URL=
WORK_DIR=/var/tmp
LOCK_FILE=/run/gnomonitoring-pg-backup.lock
DUMP_TIMEOUT=4h
FREE_SPACE_PERCENT=100
FREE_SPACE_MARGIN_MB=1024

STEP=start
TMP_DIR=
WEBHOOK_READY=false

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

usage() {
	sed -n '/^# Usage:/,/^# On any failure/p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

# json_escape prints $1 as the body of a JSON string. Control characters
# other than newline are dropped: they never carry meaning in these messages.
json_escape() {
	local s=$1
	s=${s//\\/\\\\}
	s=${s//\"/\\\"}
	s=${s//$'\n'/\\n}
	s=${s//[$'\001'-$'\037']/}
	printf '%s' "$s"
}

# curl_secret_url calls curl on a URL that is a credential (webhook, heartbeat).
# The URL goes through curl's config on stdin, so it never shows on argv
# (ps, /proc/<pid>/cmdline). load_config restricts its characters, so it
# cannot break out of the quoted config value.
curl_secret_url() {
	local url=$1
	shift
	printf 'url = "%s"\n' "$url" | curl --fail --silent --show-error --max-time 20 \
		--retry 3 --retry-delay 5 --config - "$@" >/dev/null
}

send_discord() {
	local message=$1
	if [[ $WEBHOOK_READY != true ]]; then
		log "no valid DISCORD_WEBHOOK_URL loaded yet: notification not sent"
		return 1
	fi
	curl_secret_url "$DISCORD_WEBHOOK_URL" \
		--header 'Content-Type: application/json' \
		--data-binary "{\"content\":\"$(json_escape "$message")\",\"allowed_mentions\":{\"parse\":[]}}"
}

on_exit() {
	local status=$?
	trap - EXIT
	# Nothing below may stop the notification or change the exit status, and
	# a closed log pipe (logger gone) must not kill the notification.
	set +e
	trap '' PIPE
	# A signal bash does not trap (HUP, PIPE) reaches here with status 0: only
	# a run that reached its end counts as a success.
	if ((status == 0)) && [[ $STEP != finished ]]; then
		status=1
	fi
	if [[ -n $TMP_DIR ]]; then
		rm -rf -- "$TMP_DIR" || log "could not remove $TMP_DIR"
	fi
	if ((status != 0)); then
		log "backup FAILED at step '$STEP' (exit $status)"
		send_discord "🔴 **gnomonitoring Postgres backup FAILED** on \`$(hostname)\`
step: \`$STEP\`, exit status $status, $(date -u +%Y-%m-%dT%H:%M:%SZ)
details: \`journalctl -t gnomonitoring-pg-backup\` on the server" ||
			log "the failure notification could not be sent"
	fi
	exit "$status"
}
trap on_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# load_config reads KEY=VALUE lines without executing them (no `source`):
# blank lines and # comments are skipped, one pair of quotes and trailing
# blanks are stripped. The file may hold a credential: it must be owned by the
# current user with no permission for group or others.
#
# Errors are collected first and raised only after DISCORD_WEBHOOK_URL has been
# read, so that a later typo in the file is notified instead of silently
# stopping the backups. Nothing is read from a file someone else owns: its
# webhook could point anywhere.
load_config() {
	local file=$1 line key value n=0 error='' owner mode
	[[ -f $file && ! -L $file ]] || die "$file is missing or is not a regular file"
	owner=$(stat -c %u -- "$file")
	[[ $owner == "$(id -u)" ]] || die "$file must be owned by $(id -un)"
	mode=$(stat -c %a -- "$file")
	(((8#$mode & 8#077) == 0)) || error="$file must not be accessible by group or others (chmod 600 $file)"
	while IFS= read -r line || [[ -n $line ]]; do
		n=$((n + 1))
		line=${line%$'\r'}
		[[ $line =~ ^[[:space:]]*(#|$) ]] && continue
		# The line may hold a secret: report its number, never its content.
		if ! [[ $line =~ ^([A-Z][A-Z0-9_]*)=(.*)$ ]]; then
			error=${error:-"$file:$n: expected KEY=VALUE"}
			continue
		fi
		key=${BASH_REMATCH[1]}
		value=${BASH_REMATCH[2]}
		value=${value%"${value##*[![:space:]]}"}
		if [[ $value =~ ^\"(.*)\"$ || $value =~ ^\'(.*)\'$ ]]; then
			value=${BASH_REMATCH[1]}
		fi
		case $key in
		PG_CONTAINER | PG_USER | PG_DB | S3_BUCKET | S3_PREFIX | S3_ENDPOINT | S3_REGION | AWS_PROFILE | \
			AGE_RECIPIENTS_FILE | DISCORD_WEBHOOK_URL | NOTIFY_ON_SUCCESS | HEARTBEAT_URL | WORK_DIR | \
			LOCK_FILE | DUMP_TIMEOUT | FREE_SPACE_PERCENT | FREE_SPACE_MARGIN_MB)
			printf -v "$key" '%s' "$value"
			;;
		*) error=${error:-"$file:$n: unknown key $key"} ;;
		esac
	done <"$file"
	if [[ -n $DISCORD_WEBHOOK_URL ]] && valid_url "$DISCORD_WEBHOOK_URL"; then
		WEBHOOK_READY=true
	fi
	[[ -z $error ]] || die "$error"
}

# check_recipients fails unless $1 is a regular file owned by the current user,
# not writable by group or others, holding only native age X25519 public keys
# (comments and blank lines aside).
check_recipients() {
	local file=$1 owner mode line n=0 keys=0
	[[ -f $file && ! -L $file ]] || die "AGE_RECIPIENTS_FILE $file is missing or is not a regular file"
	# Whoever can rewrite the recipients can read every later backup.
	owner=$(stat -c %u -- "$file")
	[[ $owner == "$(id -u)" ]] || die "$file must be owned by $(id -un)"
	mode=$(stat -c %a -- "$file")
	(((8#$mode & 8#022) == 0)) || die "$file must not be writable by group or others"
	while IFS= read -r line || [[ -n $line ]]; do
		n=$((n + 1))
		line=${line%$'\r'}
		[[ $line =~ ^[[:space:]]*(#|$) ]] && continue
		# A plugin recipient (age1<name>1...) would make age run an
		# age-plugin-<name> binary as root: only X25519 keys are accepted.
		[[ $line =~ ^age1[02-9ac-hj-np-z]{58}$ ]] || die "$file:$n: not an age X25519 public key (age1...)"
		keys=$((keys + 1))
	done <"$file"
	((keys > 0)) || die "$file holds no age public key"
	RECIPIENT_COUNT=$keys
}

# duration_seconds converts a DUMP_TIMEOUT value (3600, 90m, 4h, 1d) to seconds.
duration_seconds() {
	local n=$((10#${1%[smhd]}))
	case $1 in
	*d) echo $((n * 86400)) ;;
	*h) echo $((n * 3600)) ;;
	*m) echo $((n * 60)) ;;
	*) echo "$n" ;;
	esac
}

# valid_url accepts https URLs, and plain http only on the loopback (local tests).
valid_url() {
	[[ $1 =~ ^(https://[A-Za-z0-9.-]+|http://(127\.0\.0\.1|localhost))(:[0-9]+)?(/[A-Za-z0-9._~%/?\&=+:-]*)?$ ]]
}

validate_config() {
	local name
	[[ -n $DISCORD_WEBHOOK_URL ]] || die "DISCORD_WEBHOOK_URL is required"
	valid_url "$DISCORD_WEBHOOK_URL" || die "DISCORD_WEBHOOK_URL must be an https URL"
	for name in PG_CONTAINER PG_USER PG_DB; do
		[[ ${!name} =~ ^[A-Za-z0-9_.-]+$ ]] || die "$name has an invalid value"
	done
	[[ $S3_BUCKET =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || die "S3_BUCKET has an invalid value"
	if [[ -n $S3_PREFIX && $S3_PREFIX != */ ]]; then
		S3_PREFIX+=/
	fi
	[[ $S3_PREFIX =~ ^([A-Za-z0-9._-]+/)*$ ]] || die "S3_PREFIX has an invalid value"
	valid_url "$S3_ENDPOINT" || die "S3_ENDPOINT must be an https URL"
	[[ $S3_REGION =~ ^[a-z0-9-]+$ ]] || die "S3_REGION has an invalid value"
	[[ -z $AWS_PROFILE || $AWS_PROFILE =~ ^[A-Za-z0-9_.-]+$ ]] || die "AWS_PROFILE has an invalid value"
	if [[ -n $HEARTBEAT_URL ]]; then
		valid_url "$HEARTBEAT_URL" || die "HEARTBEAT_URL must be an https URL"
	fi
	[[ $NOTIFY_ON_SUCCESS =~ ^(true|false)$ ]] || die "NOTIFY_ON_SUCCESS must be true or false"
	[[ $DUMP_TIMEOUT =~ ^[0-9]+[smhd]?$ ]] || die "DUMP_TIMEOUT must look like 4h, 90m or 3600"
	(($(duration_seconds "$DUMP_TIMEOUT") > 0)) || die "DUMP_TIMEOUT must be greater than zero"
	[[ $FREE_SPACE_PERCENT =~ ^[0-9]+$ ]] || die "FREE_SPACE_PERCENT must be an integer"
	[[ $FREE_SPACE_MARGIN_MB =~ ^[0-9]+$ ]] || die "FREE_SPACE_MARGIN_MB must be an integer"
	[[ -d $WORK_DIR ]] || die "WORK_DIR $WORK_DIR does not exist"
	check_recipients "$AGE_RECIPIENTS_FILE"
	if [[ -n $AWS_PROFILE ]]; then
		export AWS_PROFILE
	else
		unset AWS_PROFILE
	fi
}

aws_s3() {
	aws --endpoint-url "$S3_ENDPOINT" --region "$S3_REGION" "$@"
}

psql_value() {
	docker exec "$PG_CONTAINER" psql -X -A -t -q -w -v ON_ERROR_STOP=1 \
		-U "$PG_USER" -d "$PG_DB" -c "$1"
}

preflight() {
	local tool running avail needed stale
	STEP=preflight
	for tool in docker age aws curl flock timeout df stat mktemp hostname find sha256sum; do
		command -v "$tool" >/dev/null || die "required tool not found: $tool"
	done
	age -R "$AGE_RECIPIENTS_FILE" -o /dev/null </dev/null || die "age rejects $AGE_RECIPIENTS_FILE"
	log "encrypting to $RECIPIENT_COUNT age recipient(s), recipients file sha256 $(sha256sum <"$AGE_RECIPIENTS_FILE" | cut -c 1-16)"
	running=$(docker inspect --format '{{.State.Running}}' "$PG_CONTAINER" 2>/dev/null) || true
	[[ $running == true ]] || die "container $PG_CONTAINER is not running"
	docker exec "$PG_CONTAINER" timeout --version >/dev/null || die "no timeout command in $PG_CONTAINER"
	DB_SIZE=$(psql_value 'SELECT pg_database_size(current_database())') ||
		die "cannot query database $PG_DB as $PG_USER in $PG_CONTAINER"
	[[ $DB_SIZE =~ ^[0-9]+$ ]] || die "unexpected database size answer"
	log "database $PG_DB: $((DB_SIZE / 1024 / 1024)) MiB on disk, $(docker exec "$PG_CONTAINER" pg_dump --version)"
	aws_s3 s3api head-bucket --bucket "$S3_BUCKET" >/dev/null ||
		die "bucket $S3_BUCKET is not reachable at $S3_ENDPOINT"
	# The dump is compressed and holds no index data, so the database size on
	# disk is a safe upper bound for the encrypted file.
	avail=$(df -B1 --output=avail -- "$WORK_DIR" | tail -n 1 | tr -d ' ')
	needed=$((DB_SIZE * FREE_SPACE_PERCENT / 100 + FREE_SPACE_MARGIN_MB * 1024 * 1024))
	((avail >= needed)) ||
		die "not enough free space in $WORK_DIR: $((avail / 1024 / 1024)) MiB free, $((needed / 1024 / 1024)) MiB needed"
	stale=$(find "$WORK_DIR" -maxdepth 1 -type d -name 'gnomonitoring-pg-backup.*' -mmin +1440 | wc -l)
	((stale == 0)) ||
		log "WARNING: $stale gnomonitoring-pg-backup.* directories older than a day in $WORK_DIR (a run was killed); remove them"
	log "pre-flight OK: bucket $S3_BUCKET reachable, $((avail / 1024 / 1024)) MiB free in $WORK_DIR"
}

run_backup() {
	local stamp name file key size remote started dump_status age_status app limit
	STEP=dump
	TMP_DIR=$(mktemp -d -- "$WORK_DIR/gnomonitoring-pg-backup.XXXXXX")
	stamp=$(date -u +%Y%m%dT%H%M%SZ)
	name="gnomonitoring-$stamp.dump.age"
	file="$TMP_DIR/$name"
	key="$S3_PREFIX$name"
	started=$SECONDS

	app="gnomonitoring-pg-backup-$stamp"
	limit=$(duration_seconds "$DUMP_TIMEOUT")

	# pg_dump reads one consistent snapshot. -w never waits for a password
	# prompt; the lock timeout fails the run instead of queueing it forever
	# behind a schema change. The time limit runs inside the container: killing
	# the docker exec client alone leaves pg_dump running there, holding its
	# snapshot and table locks. The outer timeout only backstops a hung client.
	set +e
	timeout --kill-after=30 $((limit + 120)) \
		docker exec -e PGAPPNAME="$app" "$PG_CONTAINER" \
		timeout --kill-after=30 "$limit" \
		pg_dump -w -U "$PG_USER" -d "$PG_DB" -Fc --lock-wait-timeout=10min \
		2>"$TMP_DIR/pg_dump.stderr" |
		age -R "$AGE_RECIPIENTS_FILE" -o "$file"
	dump_status=${PIPESTATUS[0]} age_status=${PIPESTATUS[1]}
	set -e
	if [[ -s $TMP_DIR/pg_dump.stderr ]]; then
		sed 's/^/pg_dump: /' "$TMP_DIR/pg_dump.stderr" >&2
	fi
	if ((dump_status != 0 || age_status != 0)); then
		# Whatever failed, end the dump's server session so that no orphaned
		# pg_dump keeps its locks until the next run.
		psql_value "SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE application_name = '$app'" \
			>/dev/null 2>&1 || log "could not check for a leftover pg_dump session ($app)"
	fi
	# age is checked first: when it fails, the dump side only dies of SIGPIPE.
	((age_status == 0)) || die "age encryption failed (exit $age_status)"
	((dump_status != 124 && dump_status != 137)) || die "pg_dump exceeded DUMP_TIMEOUT=$DUMP_TIMEOUT"
	((dump_status == 0)) || die "pg_dump failed (exit $dump_status)"
	size=$(stat -c %s -- "$file")
	log "dump encrypted: $name, $size bytes, $((SECONDS - started)) s"

	STEP=upload
	started=$SECONDS
	aws_s3 s3 cp --only-show-errors "$file" "s3://$S3_BUCKET/$key"
	log "uploaded to s3://$S3_BUCKET/$key in $((SECONDS - started)) s"

	STEP=verify
	remote=$(aws_s3 s3api head-object --bucket "$S3_BUCKET" --key "$key" --query ContentLength --output text)
	[[ $remote == "$size" ]] || die "stored object is $remote bytes, expected $size"
	log "verified: stored object is $remote bytes"

	STEP=notify
	if [[ $NOTIFY_ON_SUCCESS == true ]]; then
		send_discord "🟢 gnomonitoring Postgres backup OK on \`$(hostname)\`: \`s3://$S3_BUCKET/$key\`, $((size / 1024 / 1024)) MiB" ||
			log "the success notification could not be sent"
	fi
	if [[ -n $HEARTBEAT_URL ]]; then
		curl_secret_url "$HEARTBEAT_URL" || log "the heartbeat could not be sent"
	fi
	log "backup OK: s3://$S3_BUCKET/$key"
}

main() {
	while (($#)); do
		case $1 in
		--config)
			(($# >= 2)) || usage
			CONFIG_FILE=$2
			shift 2
			;;
		--check)
			CHECK_ONLY=true
			shift
			;;
		*) usage ;;
		esac
	done

	STEP=config
	load_config "$CONFIG_FILE"
	validate_config

	STEP=lock
	exec 9>>"$LOCK_FILE"
	flock -n 9 || die "another backup run holds $LOCK_FILE"

	preflight
	if [[ $CHECK_ONLY == true ]]; then
		log "check only: no dump taken"
	else
		run_backup
	fi
	STEP=finished
}

main "$@"
