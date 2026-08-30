#!/bin/bash
# Backend smoke test for tui-containers, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-containers on PATH).
#
# What it proves is that the tool reads the machine's *real* engines and agrees
# with their own tooling — not that a fake renders. The lab already covers
# --version and a --demo frame; this covers the backends.
#
# Everything here is read-only, and deliberately more so than in the other
# tools of the family: nothing is started, nothing is removed, and **no image
# is ever pulled**. A suite that pulled `hello-world` would need a network, a
# registry that answers, and disk it does not own, and it would leave the guest
# different from how it found it.
#
# Three shapes of machine are asserted, because all three are normal:
#
#   no engine       None of the three lab images ships docker or podman. That
#                   is not a failure: --check exits 0, reports an empty engine
#                   list and no containers, and says so. This is the path the
#                   lab actually exercises today, and it is the one most likely
#                   to regress, because it is the one nobody develops against.
#   podman only     Fedora and Arch install it in a line. Rootless is read with
#                   no privileges at all; root's scope is read through `sudo -n`
#                   where that works and reported as unread where it does not.
#   docker          Reading needs whatever the socket needs, so the tool either
#                   reaches it directly or retries once through `sudo -n` — and
#                   the report says which.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-containers}"
# TOOL is the manifest name, which is what a compatibility result is keyed on.
TOOL=tui-containers
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_absent is the inverse of a grep assertion: the command must succeed and
# its output must NOT contain the pattern. It is what proves a read stayed a
# read, which is a claim about something that did not happen.
check_absent() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && ! grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_equal compares two numbers the tool and the engine each produced.
check_equal() {
  local label="$1" got="$2" want="$3"
  if [[ -n $got && $got == "$want" ]]; then
    printf 'PASS  %s (%s)\n' "$label" "$want"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s: the tool says %s, the engine says %s\n' \
      "$label" "${got:-none}" "$want"
    fail=$((fail + 1))
  fi
}

# field reads one top-level number out of the --check report.
field() {
  sed -n "s/.*\"$1\": \([0-9]*\).*/\1/p" <<<"$2" | head -1
}

# --- compatibility evidence -------------------------------------------------
#
# The manifest's `tested` lists are generated, not claimed: they are rebuilt
# from compat/results.jsonl by tui-kit/tools/compat-sync.py, and this is where
# the lines of that file come from. The versions recorded are the ones the tool
# itself probed, read back out of --check, so they describe the machine that
# really ran the suite rather than what the tester assumed was installed.
#
# On a guest with no engine at all nothing is recorded, which is correct: there
# is no version to claim compatibility with, and a line saying so would be
# evidence of nothing.
record_compat() {
  local report="$1" outcome="$2" distro today block recorded=0
  block=$(sed -n '/"compat": \[/,/^  \]/p' <<<"$report")
  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)

  while read -r backend version; do
    [[ -z $backend || -z $version ]] && continue
    local line
    line=$(printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}' \
      "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version")
    printf 'compat-result: %s\n' "$line"
    if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
      printf '%s\n' "$line" >>"$TUI_COMPAT_RESULTS"
    fi
    recorded=$((recorded + 1))
  done < <(awk '
    /"backend":/ { gsub(/[",]/, ""); b = $2 }
    /"version":/ { gsub(/[",]/, ""); if (b != "") { print b, $2; b = "" } }
  ' <<<"$block")

  if [[ $recorded -eq 0 ]]; then
    echo "      no engine version was probed, so no compatibility result is recorded"
  fi
}

echo "--- tui-containers smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"

# What is actually installed, decided the way the tool decides it: the binary
# first, then whether it answers.
if command -v docker >/dev/null; then docker_bin=yes; else docker_bin=no; fi
if command -v podman >/dev/null; then podman_bin=yes; else podman_bin=no; fi
if sudo -n true 2>/dev/null; then privileged=yes; else privileged=no; fi
echo "      docker=$docker_bin podman=$podman_bin sudo -n=$privileged"

report=$("$bin" --check 2>&1)
status=$?

# 1. The read path works at all, as the plain lab user, and names the backend
#    it drove. It exits 0 on every shape of machine, engines included or not:
#    a server that runs no containers is a normal server, and a non-zero exit
#    there would make this suite fail on every guest the lab actually has.
check "check reads the engines unprivileged" \
  "$bin --check" \
  '"backend": "host"'

if [[ $status -ne 0 ]]; then
  printf 'FAIL  --check exited %d\n' "$status"
  fail=$((fail + 1))
fi

engine_count=$(field engineCount "$report")

# 2. The no-engine path, which is what the three lab images ship today. It is
#    asserted explicitly because it is the one nobody develops against: the
#    engine list is empty, every count is a zero that is present rather than
#    absent, and the tool still exits 0.
if [[ $docker_bin == no && $podman_bin == no ]]; then
  check "no engine is reported as installed" "$bin --check" '"engines": \[\]'
  check "no engine answered" "$bin --check" '"engineCount": 0'
  check "no container was invented" "$bin --check" '"containers": 0'
  check "no image was invented" "$bin --check" '"images": 0'
  # Every state is still reported, so a script has a zero to compare against
  # rather than a key that may or may not be there.
  check "the states are still enumerated" "$bin --check" '"running": 0'
  check "the model is present and empty" "$bin --check" '"backend": "host"'
  check_absent "nothing claims an engine version" "$bin --check" \
    '"engine": "(docker|podman)"'
fi

# 3. Docker, when it is here. The container count has to agree with dockerd's
#    own, which is the assertion that catches a list that half-worked — the
#    `--format json` shorthand is gated on the version, and both spellings have
#    to produce the same set.
if [[ $docker_bin == yes ]]; then
  check "docker is named in the report" "$bin --check" '"engine": "docker"'
  if docker info >/dev/null 2>&1 || sudo -n docker info >/dev/null 2>&1; then
    check "docker answered" "$bin --check" '"available": true'
    if docker ps -aq >/dev/null 2>&1; then
      want=$(docker ps -aq | grep -c .)
    else
      want=$(sudo -n docker ps -aq | grep -c .)
    fi
    got=$(sed -n 's/.*"containers": \([0-9]*\).*/\1/p' <<<"$report" | head -1)
    # Podman may also be here and its containers are in the same count, so the
    # comparison only holds when docker is the only engine.
    if [[ $podman_bin == no ]]; then
      check_equal "the containers match docker ps -a" "$got" "$want"
    else
      echo "      podman is here too, so the totals are not compared"
    fi
  else
    # An installed CLI with no daemon behind it is a normal machine, and the
    # report has to say so with a reason rather than showing an empty list.
    check "docker is reported as not answering" "$bin --check" '"available": false'
    check "a reason is given" "$bin --check" '"detail": ".+"'
  fi
fi

# 4. Podman, when it is here. Rootless is read with no privileges at all, which
#    is what running this suite as the plain lab user proves.
if [[ $podman_bin == yes ]]; then
  check "podman is named in the report" "$bin --check" '"engine": "podman"'
  check "the rootless scope is named" "$bin --check" '"scope": "user"'
  if podman info >/dev/null 2>&1; then
    check "the rootless scope answered" "$bin --check" '"rootless": true'
    # `podman ps` is parsed even on an empty store: `[]` is a valid answer and
    # a scope with no containers is an ordinary scope. Nothing is pulled to
    # make one.
    want=$(podman ps -a -q 2>/dev/null | grep -c .)
    if [[ $docker_bin == no ]]; then
      got=$(sed -n 's/.*"containers": \([0-9]*\).*/\1/p' <<<"$report" | head -1)
      # An infra container is dropped by the tool on purpose, and a lab guest
      # has no pods, so the two counts are the same here.
      check_equal "the containers match podman ps -a" "$got" "$want"
    fi
    if [[ $want -eq 0 ]]; then
      echo "      the rootless store is empty, which is parsed rather than skipped"
    fi
  fi

  # The system scope: read through `sudo -n` where that works, and reported as
  # unread with the reason where it does not. Both are correct outcomes and
  # the report must not confuse them.
  if [[ $privileged == yes ]]; then
    check "the system scope is named" "$bin --check" '"scope": "system"'
  else
    check "the system scope says why it was not read" "$bin --check" \
      'sudo -n|not read'
  fi
fi

# 5. The engine count agrees with what is installed, so a report that quietly
#    lost an engine is caught.
expected=0
[[ $docker_bin == yes ]] && docker info >/dev/null 2>&1 && expected=$((expected + 1))
[[ $podman_bin == yes ]] && podman info >/dev/null 2>&1 && expected=$((expected + 1))
if [[ $privileged == yes && $podman_bin == yes ]] && sudo -n podman info >/dev/null 2>&1; then
  expected=$((expected + 1))
fi
if [[ -n $engine_count && $engine_count -ge $expected ]]; then
  printf 'PASS  %s engine scope(s) answered, at least the %s expected\n' \
    "$engine_count" "$expected"
  pass=$((pass + 1))
else
  printf 'FAIL  %s engine scope(s) answered, expected at least %s\n' \
    "${engine_count:-none}" "$expected"
  fail=$((fail + 1))
fi

# 6. The findings are counts, not errors: a container that has been failing for
#    a month is a fact about the machine and this suite still passes.
check "the attention count is reported" "$bin --check" '"attentionCount": [0-9]+'
check "the unhealthy count is reported" "$bin --check" '"unhealthyCount": [0-9]+'
check "the dangling images are counted" "$bin --check" '"dangling": [0-9]+'

# 7. --check must never change anything. Nothing is started, nothing removed,
#    and no image pulled — so the engines' own inventories are identical after
#    a read.
before=""
after=""
if [[ $docker_bin == yes ]]; then
  before+=$(docker ps -aq 2>/dev/null; docker images -q 2>/dev/null)
fi
if [[ $podman_bin == yes ]]; then
  before+=$(podman ps -aq 2>/dev/null; podman images -q 2>/dev/null)
fi
$bin --check >/dev/null 2>&1
if [[ $docker_bin == yes ]]; then
  after+=$(docker ps -aq 2>/dev/null; docker images -q 2>/dev/null)
fi
if [[ $podman_bin == yes ]]; then
  after+=$(podman ps -aq 2>/dev/null; podman images -q 2>/dev/null)
fi
if [[ "$before" == "$after" ]]; then
  printf 'PASS  --check left every engine untouched\n'
  pass=$((pass + 1))
else
  printf 'FAIL  --check changed a container or an image list\n'
  fail=$((fail + 1))
fi

# 8. And it prints no mutation: --check reports the read path, and a command
#    line in its output would mean it had built one.
check_absent "--check builds no command" \
  "$bin --check" \
  '(docker|podman) (rm|rmi|stop|start|kill|pause|update|pull)|system prune|image prune|volume prune'

# 9. Nor does it reach a registry. A pull in a read path would be a network
#    connection this tool promises never to open.
check_absent "--check pulls nothing" "$bin --check" 'Pulling from|Digest: sha256'

if [[ $fail -eq 0 ]]; then
  record_compat "$report" pass
else
  record_compat "$report" fail
fi

echo "--- tui-containers: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
