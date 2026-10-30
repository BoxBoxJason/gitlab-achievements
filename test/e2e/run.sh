#!/usr/bin/env bash
# Runs the catalog end-to-end suite against a throwaway GitLab container.
#
#   test/e2e/run.sh                       # gitlab-ee:19.5.1-ee.0, torn down after
#   GITLAB_IMAGE=docker.io/gitlab/gitlab-ce:latest test/e2e/run.sh
#   KEEP=1 test/e2e/run.sh                # leave the container up to poke at
#   REUSE=1 test/e2e/run.sh               # run against the container KEEP left
#
# Needs podman or docker, Go, and about 4 GiB of free memory. GitLab takes
# around five minutes to boot; the suite itself a few more.
set -euo pipefail

cd "$(dirname "$0")/../.."

GITLAB_IMAGE=${GITLAB_IMAGE:-docker.io/gitlab/gitlab-ee:19.5.1-ee.0}
ENGINE=${ENGINE:-$(command -v podman >/dev/null && echo podman || echo docker)}
NAME=${NAME:-gitlab-achievements-e2e}
GITLAB_PORT=${GITLAB_PORT:-8929}
APP_PORT=${APP_PORT:-18080}
ARTIFACTS=${ARTIFACTS:-$PWD/e2e-artifacts}
STATE=${STATE:-$ARTIFACTS/.state}

mkdir -p "$ARTIFACTS"

# How the container reaches the host the app listens on, under a name of
# its own. Docker's host-gateway is the bridge address. Under rootless
# podman's pasta, host.containers.internal is link-local (169.254.1.2) and
# the host's primary address is also the container's own, both of which
# GitLab's URL blocker treats specially, so pasta is asked to map the host's
# loopback onto an ordinary private address instead.
HOST_ALIAS=e2e-host.internal
if [[ $ENGINE == podman ]]; then
	HOST_FLAGS=(--network "pasta:--map-host-loopback,10.0.2.2" --add-host="$HOST_ALIAS:10.0.2.2")
else
	HOST_FLAGS=(--add-host="$HOST_ALIAS:host-gateway")
fi

teardown() {
	if [[ -z ${KEEP:-} ]]; then
		"$ENGINE" logs "$NAME" >"$ARTIFACTS/gitlab-container.log" 2>&1 || true
		"$ENGINE" rm -f "$NAME" >/dev/null 2>&1 || true
	fi
}

if [[ -z ${REUSE:-} ]]; then
	"$ENGINE" rm -f "$NAME" >/dev/null 2>&1 || true
	trap teardown EXIT

	# Everything GitLab runs that the suite does not need is switched off,
	# which is what lets it fit a CI runner. Puma in single mode, Sidekiq
	# kept because it is what delivers webhooks. The memory watchdog is off
	# because in single mode it restarts Puma, i.e. all of GitLab, as soon as
	# it passes ~1.6 GB, which a run of the suite does. Application settings
	# are not cached, so the suite allowing hooks to the local network takes
	# effect at once rather than a minute later, after it has already acted.
	"$ENGINE" run -d --name "$NAME" --hostname localhost \
		-p "127.0.0.1:$GITLAB_PORT:$GITLAB_PORT" --shm-size 256m "${HOST_FLAGS[@]}" \
		-e GITLAB_OMNIBUS_CONFIG="external_url 'http://localhost:$GITLAB_PORT';
			puma['worker_processes'] = 0;
			gitlab_rails['env'] = { 'GITLAB_MEMORY_WATCHDOG_ENABLED' => 'false' };
			gitlab_rails['application_settings_cache_seconds'] = 0;
			sidekiq['concurrency'] = 10;
			prometheus_monitoring['enable'] = false;
			registry['enable'] = false;
			gitlab_kas['enable'] = false;
			gitlab_rails['usage_ping_enabled'] = false;
			gitlab_rails['gitlab_default_projects_features_container_registry'] = false" \
		"$GITLAB_IMAGE" >/dev/null
fi

GITLAB_URL="http://127.0.0.1:$GITLAB_PORT"

# The sign-in page rather than /-/readiness, which only answers the
# container's own loopback.
echo "waiting for GitLab at $GITLAB_URL ..."
for _ in $(seq 1 180); do
	if curl -fsS -o /dev/null "$GITLAB_URL/users/sign_in" 2>/dev/null; then
		break
	fi
	sleep 5
done
curl -fsS -o /dev/null "$GITLAB_URL/users/sign_in"

# An admin token can only be minted from inside. One runner call, which
# boots Rails once; the token is kept for REUSE runs.
if [[ -n ${REUSE:-} && -s $STATE ]]; then
	TOKEN=$(cat "$STATE")
else
	TOKEN="glpat-e2e$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' | head -c 24)"
	"$ENGINE" exec "$NAME" gitlab-rails runner "
		user = User.find_by_username!('root')
		token = user.personal_access_tokens.build(
			name: 'e2e', scopes: %w[api sudo admin_mode], expires_at: 30.days.from_now,
			organization: user.organizations.first || Organizations::Organization.first)
		token.set_token('$TOKEN')
		token.save!
	"
	echo "$TOKEN" >"$STATE"
	chmod 600 "$STATE"
fi

# The API can lag readiness by a little while the token cache warms.
for _ in $(seq 1 30); do
	if curl -fsS -o /dev/null -H "PRIVATE-TOKEN: $TOKEN" "$GITLAB_URL/api/v4/version"; then
		break
	fi
	sleep 2
done

go build -o "$ARTIFACTS/gitlab-achievements" .

E2E_GITLAB_URL=$GITLAB_URL \
	E2E_GITLAB_TOKEN=$TOKEN \
	E2E_APP_BIN=$ARTIFACTS/gitlab-achievements \
	E2E_APP_PORT=$APP_PORT \
	E2E_WEBHOOK_URL="http://$HOST_ALIAS:$APP_PORT" \
	E2E_ARTIFACTS=$ARTIFACTS \
	go test -tags e2e -count=1 -timeout 45m -v ./test/e2e/ "$@"
