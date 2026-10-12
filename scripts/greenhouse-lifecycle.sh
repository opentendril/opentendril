rootless_docker() {
  "$RUNUSER_BIN" -u tendril -- "$ENV_BIN" -i \
    PATH=/usr/bin:/bin \
    HOME=/home/tendril USER=tendril LOGNAME=tendril \
    XDG_RUNTIME_DIR="/run/user/${STEM_UID}" DOCKER_HOST="${DOCKER_HOST}" \
    "$DOCKER_BIN" --host="${DOCKER_HOST}" "$@"
}

verify_rootless_daemon() {
  _options=$(rootless_docker info --format '{{.SecurityOptions}}' 2>&1) ||
    fail "the tendril rootless Docker daemon is unavailable at ${DOCKER_HOST}: ${_options}"
  case "$_options" in
    *rootless*) ;;
    *) fail "refusing lifecycle operation: tendril Docker is not rootless (SecurityOptions: ${_options})" ;;
  esac
}

require_stem_socket() {
  [ -S "${SOCKET_CHECK_PATH}" ] || fail "the governed Stem Unix socket is missing or not a socket: ${STEM_SOCKET}; start the configured Stem first (TCP is never selected)"
}

get_image_id() {
  _image_id=$(rootless_docker image inspect --format '{{.Id}}' "${IMAGE}" 2>/dev/null) ||
    fail "verified installed Greenhouse image is unavailable in tendril rootless Docker: ${IMAGE}"
  [ "$_image_id" = "${IMAGE_ID}" ] || fail "installed Greenhouse tag no longer identifies the verified release image (expected ${IMAGE_ID}, found $_image_id)"
  printf '%s\n' "${IMAGE_ID}"
}

get_container_image_id() {
  rootless_docker container inspect --format '{{.Image}}' "${CONTAINER}"
}

get_container_row() {
  rootless_docker container inspect --format '{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}|{{.Image}}' "${CONTAINER}"
}

container_present() {
  _names=$(rootless_docker container ls --all --filter "name=^/${CONTAINER}$" --format '{{.Names}}') ||
    fail 'could not query tendril rootless Docker for the Greenhouse container'
  [ "$_names" = "${CONTAINER}" ]
}

run_fixed_container() {
  rootless_docker run --pull=never --detach \
    --name "${CONTAINER}" \
    --restart unless-stopped \
    --publish 127.0.0.1:4173:8080 \
    --read-only \
    --tmpfs /tmp \
    --tmpfs /etc/nginx/conf.d:uid=101,gid=101,mode=0755 \
    --cap-drop ALL \
    --security-opt no-new-privileges:true \
    --health-cmd 'wget -q -O /dev/null http://127.0.0.1:8080/' \
    --health-interval 30s \
    --health-timeout 5s \
    --health-retries 3 \
    --mount type=bind,source=${TRANSPORT_DIR},target=${TRANSPORT_DIR},readonly \
    --env STEM_TRANSPORT=unix \
    --env STEM_SOCKET=${STEM_SOCKET} \
    "${IMAGE_ID}" >/dev/null || fail 'failed to create the fixed Greenhouse container'
}

remove_old_container() {
  rootless_docker container rm --force "${CONTAINER}" >/dev/null || fail 'failed to replace the existing Greenhouse container'
}

start_greenhouse() {
  require_stem_socket
  verify_rootless_daemon
  _desired_id=$(get_image_id)
  if container_present; then
    _current_id=$(get_container_image_id) || fail 'could not inspect the existing Greenhouse container'
    if [ "$_current_id" != "$_desired_id" ]; then
      remove_old_container
    else
      _row=$(get_container_row) || fail 'could not read the existing Greenhouse lifecycle state'
      _state=${_row%%|*}
      if [ "$_state" = running ]; then
        printf 'Greenhouse is already running at http://127.0.0.1:4173\n'
        return 0
      fi
      rootless_docker container start "${CONTAINER}" >/dev/null || fail 'failed to start the installed Greenhouse container'
      printf 'Greenhouse started at http://127.0.0.1:4173\n'
      return 0
    fi
  fi
  run_fixed_container
  printf 'Greenhouse started at http://127.0.0.1:4173\n'
}

restart_greenhouse() {
  require_stem_socket
  verify_rootless_daemon
  _desired_id=$(get_image_id)
  if container_present; then
    _current_id=$(get_container_image_id) || fail 'could not inspect the existing Greenhouse container'
    if [ "$_current_id" != "$_desired_id" ]; then
      remove_old_container
      run_fixed_container
    else
      rootless_docker container restart "${CONTAINER}" >/dev/null || fail 'failed to restart the installed Greenhouse container'
    fi
  else
    run_fixed_container
  fi
  printf 'Greenhouse restarted at http://127.0.0.1:4173\n'
}

stop_greenhouse() {
  verify_rootless_daemon
  if ! container_present; then
    printf 'Greenhouse is absent; Stem was not changed\n'
    return 0
  fi
  _row=$(get_container_row) || fail 'could not read the existing Greenhouse lifecycle state'
  _state=${_row%%|*}
  if [ "$_state" = running ] || [ "$_state" = paused ]; then
    rootless_docker container stop "${CONTAINER}" >/dev/null || fail 'failed to stop the Greenhouse container'
    printf 'Greenhouse stopped; Stem was not changed\n'
  else
    printf 'Greenhouse is already %s; Stem was not changed\n' "$_state"
  fi
}

status_greenhouse() {
  verify_rootless_daemon
  if ! container_present; then
    printf 'Greenhouse: absent\n'
    return 0
  fi
  _row=$(get_container_row) || fail 'could not read the Greenhouse lifecycle state'
  _state=${_row%%|*}
  _rest=${_row#*|}
  _health=${_rest%%|*}
  _image=${_rest#*|}
  printf 'Greenhouse: %s (health: %s; image: %s)\n' "$_state" "$_health" "$_image"
}

check_greenhouse() {
  require_stem_socket
  verify_rootless_daemon
  _desired_id=$(get_image_id)
  container_present || fail 'no installed Greenhouse container exists; run start after the Stem socket is ready'
  _current_id=$(get_container_image_id) || fail 'could not inspect the Greenhouse image identity'
  [ "$_current_id" = "$_desired_id" ] || fail 'Greenhouse container is not using the currently installed verified image; run restart to reconcile it'
  _row=$(get_container_row) || fail 'could not read the Greenhouse lifecycle state'
  _state=${_row%%|*}
  _rest=${_row#*|}
  _health=${_rest%%|*}
  [ "$_state" = running ] || fail "Greenhouse container is not running (state: ${_state})"
  [ "$_health" = healthy ] || fail "Greenhouse container is not healthy (health: ${_health}); no Botanist authority is acquired by check"
  printf 'Greenhouse lifecycle check passed: healthy on local Unix transport\n'
}

