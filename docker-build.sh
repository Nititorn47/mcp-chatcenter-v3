#!/usr/bin/env bash
set +x
set -euo pipefail
readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly IMAGE_REPOSITORY="${1:?image repository is required}"
readonly IMAGE_TAG="${2:?image tag is required}"
readonly IMAGE="${IMAGE_REPOSITORY}:${IMAGE_TAG}"
readonly PLATFORMS="${PLATFORMS:-linux/amd64}"
readonly DOCKER_HUB_USERNAME="${3:?Docker Hub username is required}"
IFS= read -r DOCKER_HUB_TOKEN || [[ -n "${DOCKER_HUB_TOKEN}" ]]
if [[ -z "${DOCKER_HUB_TOKEN}" ]]; then
  echo "Docker Hub access token is required on stdin." >&2
  exit 1
fi
case "${DOCKER_HUB_USERNAME}" in *[!a-zA-Z0-9_-]*) exit 1 ;; esac
case "${DOCKER_HUB_TOKEN}" in *[!a-zA-Z0-9_.-]*) exit 1 ;; esac
export DOCKER_HUB_USERNAME DOCKER_HUB_TOKEN
umask 077
DOCKER_CONFIG="$(mktemp -d "${TMPDIR:-/tmp}/chatcenter-mcp-docker.XXXXXX")"
export DOCKER_CONFIG
trap 'rm -rf -- "${DOCKER_CONFIG}"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
cat > "${DOCKER_CONFIG}/docker-credential-centermcp" <<'HELPER'
#!/usr/bin/env bash
set +x
set -euo pipefail
case "${1:-}" in
  store) cat > /dev/null ;;
  get) printf '{"Username":"%s","Secret":"%s"}\n' "${DOCKER_HUB_USERNAME}" "${DOCKER_HUB_TOKEN}" ;;
  erase) ;;
  *) exit 1 ;;
esac
HELPER
chmod 700 "${DOCKER_CONFIG}/docker-credential-centermcp"
export PATH="${DOCKER_CONFIG}:${PATH}"
printf '%s\n' '{"credsStore":"centermcp"}' > "${DOCKER_CONFIG}/config.json"
printf '%s' "${DOCKER_HUB_TOKEN}" | docker login --username "${DOCKER_HUB_USERNAME}" --password-stdin
echo "Building and pushing ${IMAGE} for ${PLATFORMS}..."
docker buildx build --platform "${PLATFORMS}" --pull --tag "${IMAGE}" --push "${SCRIPT_DIR}"
echo "Published ${IMAGE}"
