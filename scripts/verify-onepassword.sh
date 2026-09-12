#!/usr/bin/env bash
#
# End-to-end verification for `ops 1p push` / `ops 1p pull` and for the op://
# resolution the SSH layer relies on.
#
# Why a stub instead of the real CLI: `op vault list` needs an interactive
# Desktop App approval, so a non-interactive harness can never drive the real
# binary. cmd/opstub answers the handful of subcommands OpsPulse uses, which is
# enough to pin the parts that actually break:
#
#   * the item JSON travels on stdin (op rejects --template together with a
#     redirected stdin, and a child process always gets a redirected stdin)
#   * an existing item goes through `item get` + `item edit`, not a re-create
#   * a pushed password stops being stored as plaintext in servers.yaml
#   * a pulled key lands on disk as a usable 0600 key with its .pub sibling
#
# Only structural facts are printed - no key material, no plaintext password.
#
# Usage: scripts/verify-onepassword.sh
set -euo pipefail

REPO_POSIX="$(cd "$(dirname "$0")/.." && pwd)"
REPO_NATIVE="$REPO_POSIX"
case "$(uname -s)" in
	MINGW* | MSYS* | CYGWIN*) REPO_NATIVE="$(cd "$REPO_POSIX" && pwd -W)" ;;
esac

WORK_POSIX="$REPO_POSIX/.opstub-verify"
WORK_NATIVE="$REPO_NATIVE/.opstub-verify"
EXE="$(go env GOEXE)"

cleanup() { rm -rf "$WORK_POSIX"; }
trap cleanup EXIT
cleanup
# $WORK_POSIX/home is OPSPULSE_HOME (servers.yaml lives there); fakehome stands in
# for the user's home so that `ops 1p pull` never touches the real ~/.ssh.
mkdir -p "$WORK_POSIX/home" "$WORK_POSIX/fakehome/.ssh"

echo "==> building ops and the op stub"
# Build into the *native* form of the path: with MSYS path conversion disabled
# (MSYS_NO_PATHCONV=1) a /d/... target is handed to go.exe verbatim, which makes
# it resolve against the current drive and silently build somewhere else.
(cd "$REPO_POSIX" && go build -o "$WORK_NATIVE/ops$EXE" ./cmd/opspulse)
(cd "$REPO_POSIX" && go build -o "$WORK_NATIVE/opstub$EXE" ./cmd/opstub)
ssh-keygen -q -t ed25519 -N '' -f "$WORK_NATIVE/id_web" -C opspulse-verify

export OPSPULSE_HOME="$WORK_NATIVE/home"
export OPSPULSE_OP_PATH="$WORK_NATIVE/opstub$EXE"
export STUB_OP_LOG="$WORK_NATIVE/op.log"
export STUB_OP_KEY="$WORK_NATIVE/id_web"
# pull writes into ~/.ssh; redirect home so the real one is never touched
export USERPROFILE="$WORK_NATIVE/fakehome"
export HOME="$WORK_NATIVE/fakehome"
mkdir -p "$WORK_POSIX/fakehome/.ssh"

OPS="$WORK_NATIVE/ops$EXE"
YAML="$WORK_POSIX/home/servers.yaml"
LOCAL_PW="local-plaintext-pw"
STUB_PW="stub-vault-pw"
FAILURES=0

check() { # check <what> <want> <got>
	if [ "$2" = "$3" ]; then
		echo "  PASS  $1 ($3)"
	else
		echo "  FAIL  $1: want $2, got $3"
		FAILURES=$((FAILURES + 1))
	fi
}

write_config() { # write_config <web-key-path>
	cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $1
  - name: vps_01
    host: 10.0.0.11
    user: root
    password: $LOCAL_PW
  - name: bare
    host: 10.0.0.12
    user: root
YAML
}

echo
echo "==> push --all (no --vault: the only accessible vault is picked silently)"
write_config "$WORK_NATIVE/id_web"
: > "$STUB_OP_LOG"
"$OPS" 1p push --all

echo
echo "==> op invocation log"
cat "$STUB_OP_LOG"

echo
echo "==> assertions"
check "credentials rewritten as op:// references" 2 "$(grep -c 'op://' "$YAML")"
check "no plaintext password left in servers.yaml" 0 "$(grep -c "$LOCAL_PW" "$YAML")"
check "create goes through stdin ('-' argument)" 2 "$(grep -c 'item create --vault Personal -' "$STUB_OP_LOG")"
check "every create received a non-empty stdin" 2 "$(grep -c 'item create --vault Personal - | stdin=[1-9]' "$STUB_OP_LOG")"
check "SSH Key template requested" 1 "$(grep -c 'item template get SSH Key' "$STUB_OP_LOG")"
check "Login template requested" 1 "$(grep -c 'item template get Login' "$STUB_OP_LOG")"

echo
echo "==> status"
"$OPS" 1p status

echo
echo "==> re-push --all must be a no-op"
"$OPS" 1p push --all | tail -n 4

echo
echo "==> existing item must be updated, not re-created"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
  - name: vps_01
    host: 10.0.0.11
    user: root
    password: op://Personal/opspulse_vps_01_password/password
  - name: bare
    host: 10.0.0.12
    user: root
YAML
: > "$STUB_OP_LOG"
STUB_OP_EXISTING=opspulse_web "$OPS" 1p push web >/dev/null
check "item read before update" 1 "$(grep -c 'item get item-existing --vault Personal --format json' "$STUB_OP_LOG")"
check "update receives a non-empty stdin" 1 "$(grep -c 'item edit item-existing --vault Personal | stdin=[1-9]' "$STUB_OP_LOG")"

echo
echo "==> pull a password back into servers.yaml"
"$OPS" 1p pull vps_01
check "plaintext password restored" 1 "$(grep -c "password: $STUB_PW" "$YAML")"
check "password reference gone" 0 "$(grep -c 'op://Personal/opspulse_vps_01_password' "$YAML")"

echo
echo "==> pull a private key back onto disk"
"$OPS" 1p pull web
KEY="$WORK_POSIX/fakehome/.ssh/opspulse_web"
check "private key written" 1 "$([ -f "$KEY" ] && echo 1 || echo 0)"
check ".pub sibling derived" 1 "$([ -f "$KEY.pub" ] && echo 1 || echo 0)"
check "key is parseable" 0 "$(ssh-keygen -l -f "$KEY" >/dev/null 2>&1; echo $?)"
check "key_path rebound to the file" 1 "$(grep -c 'key_path: ~/.ssh/opspulse_web' "$YAML")"

echo
echo "==> a server with no credentials is skipped"
"$OPS" 1p push bare | tail -n 3

echo
if [ "$FAILURES" -eq 0 ]; then
	echo "OK: all assertions passed"
else
	echo "FAILED: $FAILURES assertion(s)"
	exit 1
fi
