#!/usr/bin/env bash
# The docker compose example, end to end: start understudy, run the app
# twice with a restart of understudy in between (the second run must count
# one more note, which shows the state survived), list the notes from the
# host, then stop everything and drop the volume.
#
#   ./demo.sh
#
# Needs Docker with the compose plugin and the AWS CLI. Set KEEP=1 to leave
# the stack running, and UNDERSTUDY_PORT to publish on another host port
# than 4566 (0 picks a free one).
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

fail() {
  echo "demo: $*" >&2
  exit 1
}

# notes_after_run runs the app once and prints how many notes it counted.
notes_after_run() {
  local out
  out=$(docker compose run --rm app)
  echo "$out" >&2
  sed -n 's/^the bucket holds \([0-9]*\) note(s)$/\1/p' <<<"$out"
}

if [[ "${KEEP:-}" != 1 ]]; then
  trap 'echo "==> stopping and removing the stack and its volume"; docker compose down --volumes --remove-orphans' EXIT
fi

echo "==> building and starting understudy (waits until its healthcheck passes)"
docker compose up --build --wait understudy
docker compose build app

echo "==> running the app"
before=$(notes_after_run)

echo "==> restarting understudy; its state is saved to the volume on shutdown"
docker compose restart understudy
docker compose up --wait understudy

echo "==> running the app again"
after=$(notes_after_run)
[[ -n "$before" && "$after" == "$((before + 1))" ]] ||
  fail "expected one more note after the restart, counted $before then $after"

published=$(docker compose port understudy 4566)
echo "==> the notes, listed from the host through the published port $published"
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1 \
  aws --endpoint-url "http://$published" s3 ls s3://compose-demo/notes/
