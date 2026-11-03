#!/usr/bin/env bash
# Restore a pg-backup-scaleway.sh backup into a throwaway postgres:16 container
# and check it: decrypt with the age private key, pg_restore, count the rows of
# every table, and optionally compare them with counts taken from the source.
#
# Run it on a workstation that holds the private key, never on the server.
# The plaintext dump only flows through a pipe into the container; the
# container has no network and is removed at the end (with its data volume)
# unless --keep is given.
#
# Usage:
#   pg-restore-test.sh --identity KEY_FILE SOURCE [options]
#
#   SOURCE, one of:
#     --file PATH          a local .dump.age file
#     --s3-key KEY         an object key in the bucket, e.g. postgres/gnomonitoring-<ts>.dump.age
#     --latest             the most recent gnomonitoring-<UTC timestamp>.dump.age
#                          object under --prefix
#
#   options:
#     --source-counts FILE pg-row-counts.sh output of the source database
#     --allow-drift        accept small count differences (the source kept
#                          receiving writes after the dump): up to 10 rows
#                          or --max-drift-pct of the source count, whichever is
#                          larger; a missing table, an empty restored table or
#                          a larger difference still fails
#     --max-drift-pct PCT  default 1 (implies --allow-drift)
#     --bucket NAME        default gnomonitoring
#     --prefix PREFIX      default postgres/
#     --endpoint URL       default https://s3.fr-par.scw.cloud
#     --region REGION      default fr-par
#     --image IMAGE        default postgres:16 (same major as production)
#     --keep               leave the restored container running for inspection
#
# The S3 sources use the aws CLI and its usual credentials (~/.aws, AWS_PROFILE).
# Downloads go to $TMPDIR (default /tmp, often a small tmpfs): point TMPDIR at
# a disk with room for one backup.
# Exit status: 0 restore and checks OK, 1 failure.
set -euo pipefail
umask 077

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

identity=
source_file=
s3_key=
latest=false
source_counts=
allow_drift=false
max_drift_pct=1
bucket=gnomonitoring
prefix=postgres/
endpoint=https://s3.fr-par.scw.cloud
region=fr-par
image=postgres:16
keep=false

container=
tmp_dir=

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() {
	log "ERROR: $*"
	exit 1
}

usage() {
	sed -n '/^# Usage:/,/^# Exit status/p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

cleanup() {
	local status=$?
	trap - EXIT
	set +e
	if [[ -n $container && $keep == false ]]; then
		# The container's volume holds the restored plaintext data: a failed
		# removal fails the run.
		if ! docker rm -f -v "$container" >/dev/null; then
			log "ERROR: could not remove $container, which holds restored production data:"
			log "remove it by hand: docker rm -f -v $container"
			status=1
		fi
	elif [[ -n $container ]]; then
		log "kept $container: docker exec -it $container psql -U gnomonitoring; remove it with docker rm -f -v $container"
	fi
	if [[ -n $tmp_dir ]]; then
		rm -rf -- "$tmp_dir"
	fi
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

while (($#)); do
	case $1 in
	--identity | --file | --s3-key | --source-counts | --bucket | --prefix | --endpoint | --region | --image | \
		--max-drift-pct)
		(($# >= 2)) || usage
		case $1 in
		--identity) identity=$2 ;;
		--file) source_file=$2 ;;
		--s3-key) s3_key=$2 ;;
		--source-counts) source_counts=$2 ;;
		--bucket) bucket=$2 ;;
		--prefix) prefix=$2 ;;
		--endpoint) endpoint=$2 ;;
		--region) region=$2 ;;
		--image) image=$2 ;;
		--max-drift-pct)
			max_drift_pct=$2
			allow_drift=true
			;;
		esac
		shift 2
		;;
	--latest)
		latest=true
		shift
		;;
	--allow-drift)
		allow_drift=true
		shift
		;;
	--keep)
		keep=true
		shift
		;;
	*) usage ;;
	esac
done

[[ -n $identity ]] || usage
[[ -f $identity ]] || die "identity file $identity not found"
sources=0
[[ -n $source_file ]] && sources=$((sources + 1))
[[ -n $s3_key ]] && sources=$((sources + 1))
[[ $latest == true ]] && sources=$((sources + 1))
((sources == 1)) || die "give exactly one of --file, --s3-key, --latest"
[[ -z $source_counts || -f $source_counts ]] || die "source counts file $source_counts not found"
[[ $max_drift_pct =~ ^[0-9]+([.][0-9]+)?$ ]] || die "--max-drift-pct must be a number"
if [[ -n $prefix && $prefix != */ ]]; then
	prefix+=/
fi
for tool in docker age; do
	command -v "$tool" >/dev/null || die "required tool not found: $tool"
done

aws_s3() {
	aws --endpoint-url "$endpoint" --region "$region" "$@"
}

tmp_dir=$(mktemp -d)

# 1. Fetch the ciphertext (safe on disk: it is encrypted).
if [[ $latest == true ]]; then
	command -v aws >/dev/null || die "required tool not found: aws"
	aws_s3 s3api list-objects-v2 --bucket "$bucket" --prefix "$prefix" \
		--query 'Contents[].Key' --output text >"$tmp_dir/keys" || die "cannot list s3://$bucket/$prefix"
	# Only names written by pg-backup-scaleway.sh count; their UTC timestamp
	# makes the last one in order the newest.
	s3_key=$(tr '\t' '\n' <"$tmp_dir/keys" |
		grep -E "^${prefix//./\\.}gnomonitoring-[0-9]{8}T[0-9]{6}Z\\.dump\\.age\$" | LC_ALL=C sort | tail -n 1) || true
	[[ -n $s3_key ]] || die "no gnomonitoring-<timestamp>.dump.age object under s3://$bucket/$prefix"
	log "latest backup: $s3_key"
fi
if [[ -n $s3_key ]]; then
	command -v aws >/dev/null || die "required tool not found: aws"
	source_file="$tmp_dir/$(basename -- "$s3_key")"
	log "downloading s3://$bucket/$s3_key"
	started=$SECONDS
	aws_s3 s3 cp --only-show-errors "s3://$bucket/$s3_key" "$source_file"
	log "downloaded $(stat -c %s -- "$source_file") bytes in $((SECONDS - started)) s"
fi
[[ -f $source_file ]] || die "backup file $source_file not found"

# 2. A throwaway server: no network, local trust auth, data in an anonymous
# volume that docker rm -v deletes.
container="gnomonitoring-restore-test-$(date -u +%Y%m%d%H%M%S)-$$"
log "starting $image as $container"
docker run -d --name "$container" --network none \
	-e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_USER=gnomonitoring -e POSTGRES_DB=gnomonitoring \
	"$image" >/dev/null
# The image first runs a socket-only init server, then restarts: wait for TCP
# on the loopback, which only the final server listens on.
for _ in $(seq 1 120); do
	if docker exec "$container" pg_isready -q -h 127.0.0.1 -U gnomonitoring -d gnomonitoring 2>/dev/null; then
		break
	fi
	sleep 1
done
docker exec "$container" pg_isready -q -h 127.0.0.1 -U gnomonitoring -d gnomonitoring ||
	die "the restore server did not start"

# 3. Decrypt and restore in one pipe. --exit-on-error makes any restore error
# fatal instead of a warning at the end.
log "decrypting and restoring $(basename -- "$source_file") ($(stat -c %s -- "$source_file") bytes)"
started=$SECONDS
set +e
age -d -i "$identity" -- "$source_file" |
	docker exec -i "$container" pg_restore -U gnomonitoring -d gnomonitoring \
		--no-owner --no-privileges --exit-on-error
statuses=("${PIPESTATUS[@]}")
set -e
# When pg_restore stops first, age only dies of SIGPIPE (141): report the restore.
((statuses[0] == 0 || statuses[0] == 141)) || die "age could not decrypt $source_file (wrong key or damaged file)"
((statuses[1] == 0)) || die "pg_restore failed (exit ${statuses[1]})"
((statuses[0] == 0)) || die "age failed (exit ${statuses[0]})"
restore_seconds=$((SECONDS - started))
log "restored in $restore_seconds s"

# 4. Count the rows of the restored database.
# Table names come from the archive: drop control characters before printing.
"$here/pg-row-counts.sh" --container "$container" | LC_ALL=C tr -cd '[:print:]\t\n' >"$tmp_dir/restored.tsv"
tables=$(wc -l <"$tmp_dir/restored.tsv")
rows=$(awk -F '\t' '{ s += $2 } END { print s + 0 }' "$tmp_dir/restored.tsv")
db_size=$(docker exec "$container" psql -X -A -t -q -U gnomonitoring -d gnomonitoring \
	-c "SELECT pg_size_pretty(pg_database_size('gnomonitoring'))")
((tables > 0)) || die "the restored database has no table"

if [[ -z $source_counts ]]; then
	printf '%-45s %15s\n' table restored
	awk -F '\t' '{ printf "%-45s %15s\n", $1, $2 }' "$tmp_dir/restored.tsv"
	log "restore OK: $tables tables, $rows rows, $db_size, restore $restore_seconds s (no source counts to compare)"
	exit 0
fi

# 5. Compare with the source counts, table by table ("-" marks a table
# present on one side only).
LC_ALL=C sort -t $'\t' -k1,1 -- "$source_counts" >"$tmp_dir/source.sorted"
LC_ALL=C sort -t $'\t' -k1,1 -- "$tmp_dir/restored.tsv" >"$tmp_dir/restored.sorted"
set +e
LC_ALL=C join -t $'\t' -a 1 -a 2 -e - -o 0,1.2,2.2 "$tmp_dir/source.sorted" "$tmp_dir/restored.sorted" |
	awk -F '\t' -v allow_drift="$allow_drift" -v max_pct="$max_drift_pct" '
	BEGIN { printf "%-45s %15s %15s %12s  %s\n", "table", "source", "restored", "delta", "status" }
	{
		delta = "-"
		if ($3 == "-") { status = "MISSING"; bad++ }
		else if ($2 == "-") { status = "EXTRA"; bad++ }
		else {
			delta = $3 - $2
			if ($2 == $3) status = "ok"
			else if ($3 == 0) { status = "EMPTY"; bad++ }
			else if (allow_drift == "true" && (delta < 0 ? -delta : delta) <= (max_pct * $2 / 100 > 10 ? max_pct * $2 / 100 : 10)) {
				status = "drift"; drift++
			}
			else { status = "DIFF"; bad++ }
		}
		printf "%-45s %15s %15s %12s  %s\n", $1, $2, $3, delta, status
	}
	END {
		printf "\n%d table(s) failed, %d drifted\n", bad, drift
		exit bad > 0
	}'
compare_status=${PIPESTATUS[1]}
set -e
((compare_status == 0)) || die "row counts do not match the source"
log "restore OK: $tables tables, $rows rows, $db_size, restore $restore_seconds s, row counts checked against the source"
