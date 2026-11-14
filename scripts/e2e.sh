#!/bin/bash
# Runs the end-to-end tests (internal/app/e2e_test.go, build tag e2e) inside a throwaway
# privileged container, never on the host: they start containers and systemd units. The
# test binary is built here; IMAGE must start dockerd with its default command:
#   docker:<version>-dind (the default): Docker Compose only, the podman, systemd and
#     recovery tests are skipped (no podman, no systemd, no restic);
#   an image with systemd as PID 1, dockerd, podman and restic: everything.
#
#   scripts/e2e.sh [IMAGE]

set -euo pipefail

default_image=docker:29.9.2-dind # renovate: datasource=docker
image=${1:-$default_image}
name="restor-e2e-$$"
tmp=$(mktemp -d)
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$tmp"' EXIT

CGO_ENABLED=0 go test -c -tags e2e -o "$tmp/e2e.test" ./internal/app
docker run -d --privileged --name "$name" "$image" >/dev/null
for _ in $(seq 90); do
    docker exec "$name" docker info >/dev/null 2>&1 && break
    sleep 1
done
if ! docker exec "$name" docker info >/dev/null 2>&1; then
    echo "dockerd did not start in $image" >&2
    docker logs "$name" 2>&1 | tail -20 >&2
    exit 1
fi
# With systemd in the image, podman compose (docker-compose provider) needs podman's API.
docker exec "$name" sh -c 'command -v systemctl >/dev/null &&
    systemctl start podman.socket >/dev/null 2>&1 || true'
docker cp "$tmp/e2e.test" "$name:/e2e.test"
docker exec -e RESTOR_E2E=1 -w / "$name" /e2e.test -test.v -test.count=1 -test.run TestE2E
