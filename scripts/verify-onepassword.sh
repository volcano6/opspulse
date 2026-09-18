#!/usr/bin/env bash
#
# End-to-end verification for `ops 1p push` / `ops 1p pull` and for the op://
# resolution the SSH layer relies on.
#
# Why a stub instead of the real CLI: `op vault list` needs an interactive
# Desktop App approval, so a non-interactive harness can never drive the real
# binary. scripts/opstub answers the handful of subcommands OpsPulse uses, which is
# enough to pin the parts that actually break:
#
#   * the item JSON travels on stdin (op rejects --template together with a
#     redirected stdin, and a child process always gets a redirected stdin)
#   * an existing item goes through `item get` + `item edit`, not a re-create
#   * a pushed password stops being stored as plaintext in servers.yaml
#   * a pulled key lands on disk as a usable 0600 key with its .pub sibling
#   * a pull that would write a plaintext password refuses in a pipe without
#     --yes, and a key-only pull never asks
#   * `pull --all` restores everything, leaves skip_batch servers alone unless
#     --include-skipped is given, and restores a dual-credential server in full
#   * a foreign key on disk blocks the restore until --force says otherwise
#
# Only structural facts are printed - no key material, no plaintext password.
#
# Usage: scripts/verify-onepassword.sh
set -euo pipefail

export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"

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
(cd "$REPO_POSIX" && go build -o "$WORK_NATIVE/opstub$EXE" ./scripts/opstub)
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

# key_fpr fingerprints the key *derived from the private key itself*.
# `ssh-keygen -l -f <private>` is not usable here: it prefers a sibling .pub
# file, so after swapping only the private key it would keep reporting the old
# key and every comparison below would pass vacuously.
#
# The copy is not incidental. ssh-keygen refuses to load a private key whose
# permissions are group- or world-readable, and the repository lives on a
# filesystem (WSL's DrvFs) where every file reads as 0777 and chmod is a no-op.
# Copying to a POSIX temp file is the only way to get a key ssh-keygen accepts.
key_fpr() {
	local staging
	staging="$(mktemp -d)"
	cp "$1" "$staging/key"
	chmod 600 "$staging/key"
	ssh-keygen -y -f "$staging/key" 2>/dev/null | ssh-keygen -lf - 2>/dev/null | awk '{print $2}'
	rm -rf "$staging"
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
check "SSH Key templates are never requested" 0 "$(grep -c 'item template get SSH Key' "$STUB_OP_LOG")"
check "Login template requested for both credentials" 2 "$(grep -c 'item template get Login' "$STUB_OP_LOG")"
# The real CLI accepts an SSH_KEY payload, exits 0, and stores nothing. The stub
# refuses it outright, so a regression back to SSH Key items fails here.
check "no payload targets the SSH_KEY category" 0 "$(grep -c 'category=SSH_KEY' "$STUB_OP_LOG")"
check "both payloads are Login items" 2 "$(grep -c 'category=LOGIN' "$STUB_OP_LOG")"

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
STUB_OP_EXISTING=opspulse_web_key "$OPS" 1p push web >/dev/null
check "item read before update" 1 "$(grep -c 'item get item-opspulse_web_key --vault Personal --format json' "$STUB_OP_LOG")"
check "update receives a non-empty stdin" 1 "$(grep -c 'item edit item-opspulse_web_key --vault Personal | stdin=[1-9]' "$STUB_OP_LOG")"

echo
echo "==> a piped pull refuses instead of hanging on a prompt"
PIPED="$(printf '' | "$OPS" 1p pull vps_01 2>&1 || true)"
check "piped pull points at --yes" 1 "$(printf '%s' "$PIPED" | grep -c -- '--yes')"
check "piped pull wrote nothing" 0 "$(grep -c "$STUB_PW" "$YAML")"

echo
echo "==> pull a password back into servers.yaml (--yes)"
"$OPS" 1p pull vps_01 --yes
check "plaintext password restored" 1 "$(grep -c "password: $STUB_PW" "$YAML")"
check "password reference gone" 0 "$(grep -c 'op://Personal/opspulse_vps_01_password' "$YAML")"

echo
echo "==> pull a private key back onto disk (key only, so no confirmation)"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: op://Personal/opspulse_web_key/opspulse_private_key
YAML
KEY="$WORK_POSIX/fakehome/.ssh/opspulse_web"
KEY_WANT="$(key_fpr "$WORK_NATIVE/id_web")"
"$OPS" 1p pull web
check "private key written" 1 "$([ -f "$KEY" ] && echo 1 || echo 0)"
check ".pub sibling derived" 1 "$([ -f "$KEY.pub" ] && echo 1 || echo 0)"
check "key is parseable" 0 "$(ssh-keygen -l -f "$KEY" >/dev/null 2>&1; echo $?)"
check "the key on disk is the 1Password copy" "$KEY_WANT" "$(key_fpr "$KEY")"
check "key_path rebound to the file" 1 "$(grep -c 'key_path: ~/.ssh/opspulse_web' "$YAML")"

echo
echo "==> a different key already on disk is not overwritten without --force"
ssh-keygen -q -t ed25519 -N '' -f "$WORK_NATIVE/id_other" -C opspulse-other
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: op://Personal/opspulse_web_key/opspulse_private_key
YAML
# Only the private key is swapped; the stale .pub from the previous pull stays
# behind on purpose, so that a check which consults it would be caught.
cp "$WORK_NATIVE/id_other" "$KEY"
FPR_OTHER="$(key_fpr "$KEY")"
if [ "$FPR_OTHER" = "$KEY_WANT" ]; then
	echo "  FAIL  the conflict fixture is not a distinct key"
	FAILURES=$((FAILURES + 1))
fi
CONFLICT="$("$OPS" 1p pull web 2>&1 || true)"
CONFLICT_RC=0
"$OPS" 1p pull web >/dev/null 2>&1 || CONFLICT_RC=$?
check "the conflict is reported with a way out" 1 "$(printf '%s' "$CONFLICT" | grep -c -- '--force')"
check "a blocked restore exits non-zero" 1 "$CONFLICT_RC"
check "the foreign key survived untouched" "$FPR_OTHER" "$(key_fpr "$KEY")"

"$OPS" 1p pull web --force >/dev/null
check "--force replaces it with the 1Password copy" "$KEY_WANT" "$(key_fpr "$KEY")"

echo
echo "==> pull --all restores everything, but honours skip_batch"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: op://Personal/opspulse_web_key/opspulse_private_key
  - name: vps_01
    host: 10.0.0.11
    user: root
    password: op://Personal/opspulse_vps_01_password/password
  - name: guarded
    host: 10.0.0.13
    user: root
    skip_batch: true
    key_path: op://Personal/opspulse_guarded_key/opspulse_private_key
  - name: bare
    host: 10.0.0.12
    user: root
YAML
rm -f "$KEY"
ALL_OUT="$("$OPS" 1p pull --all --yes 2>&1 || true)"
check "summary counts restores and skips" 1 \
	"$(printf '%s' "$ALL_OUT" | grep -c 'Pull finished: 2 restored, 0 adopted, 1 skipped, 0 blocked, 0 failed')"
check "the skip_batch server is named" 1 "$(printf '%s' "$ALL_OUT" | grep -c 'skip_batch')"
check "the skip_batch server stays in 1Password" 1 "$(grep -c 'op://Personal/opspulse_guarded' "$YAML")"

echo
echo "==> --include-skipped pulls the guarded server too"
"$OPS" 1p pull --all --include-skipped --yes >/dev/null
check "guarded key restored" 1 "$(grep -c 'key_path: ~/.ssh/opspulse_guarded' "$YAML")"

echo
echo "==> a server holding both a key and a password is restored in full"
cat > "$YAML" <<YAML
servers:
  - name: dual
    host: 10.0.0.14
    user: root
    key_path: op://Personal/opspulse_dual_key/opspulse_private_key
    password: op://Personal/opspulse_dual_password/password
YAML
"$OPS" 1p pull dual --yes >/dev/null
check "the key came back" 1 "$(grep -c 'key_path: ~/.ssh/opspulse_dual' "$YAML")"
check "the password came back too" 1 "$(grep -c "password: $STUB_PW" "$YAML")"
check "no op:// reference is stranded" 0 "$(grep -c 'op://' "$YAML")"

echo
echo "==> a server with no credentials is skipped"
cat > "$YAML" <<YAML
servers:
  - name: bare
    host: 10.0.0.12
    user: root
YAML
"$OPS" 1p push bare | tail -n 3

echo
echo "==> a write that does not stick is caught before servers.yaml is rebound"
# The real CLI accepts an SSH Key payload, exits 0, and stores nothing - a
# "successful" push that leaves the server pointing at an empty item. The stub
# reproduces the observable half of that by returning a different key on read.
# id_other is the distinct key generated by the --force section above.
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
YAML
MISMATCH_RC=0
MISMATCH="$(STUB_OP_READ_KEY="$WORK_NATIVE/id_other" "$OPS" 1p push web 2>&1)" || MISMATCH_RC=$?
check "the mismatch fails the push" 1 "$MISMATCH_RC"
check "the failure explains itself" 1 "$(printf '%s' "$MISMATCH" | grep -c 'could not read it back')"
check "servers.yaml was left unchanged" 1 "$(grep -c "key_path: $WORK_NATIVE/id_web" "$YAML")"
check "no op:// reference was written" 0 "$(grep -c 'op://' "$YAML")"

echo
echo "==> --filter selects a batch, and --include-skipped widens it"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
    labels:
      env: prod
  - name: guarded
    host: 10.0.0.13
    user: root
    skip_batch: true
    labels:
      env: prod
    key_path: $WORK_NATIVE/id_web
  - name: dev
    host: 10.0.0.15
    user: root
    labels:
      env: dev
    key_path: $WORK_NATIVE/id_web
YAML
FILTERED="$("$OPS" 1p push --filter env=prod 2>&1)"
check "the filter pushed only the unguarded match" 1 "$(printf '%s' "$FILTERED" | grep -c 'Pushed credentials for 1 server')"
check "the skip_batch match is named" 1 "$(printf '%s' "$FILTERED" | grep -c 'skip_batch')"
check "the non-matching server kept its local key" 2 "$(grep -c "key_path: $WORK_NATIVE/id_web" "$YAML")"

# Naming servers and filtering them are different instructions; refusing beats
# silently targeting the wrong set.
MIXED="$("$OPS" 1p push web --filter env=prod 2>&1 || true)"
check "names plus --filter is refused" 1 "$(printf '%s' "$MIXED" | grep -c -- '--filter')"

echo
echo "==> --from-vault adopts credentials that servers.yaml does not reference"
# web keeps a local key and vps_01 a plaintext password, while the vault holds
# both items - the cross-machine case, where servers.yaml was never rebound.
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
  - name: vps_01
    host: 10.0.0.11
    user: root
    password: $LOCAL_PW
YAML
rm -f "$KEY"
# Deliberately no --yes: adoption writes a reference, never a secret, so it must
# not trip the plaintext-password confirmation gate.
ADOPT_RC=0
STUB_OP_EXISTING=opspulse_web_key,opspulse_vps_01_password "$OPS" 1p pull --all --from-vault > "$WORK_NATIVE/adopt.out" 2>&1 || ADOPT_RC=$?
check "adoption needs no plaintext confirmation" 0 "$ADOPT_RC"
check "the key is bound to 1Password" 1 "$(grep -c 'key_path: op://Personal/opspulse_web_key/opspulse_private_key' "$YAML")"
check "the password is bound to 1Password" 1 "$(grep -c 'password: op://Personal/opspulse_vps_01_password/password' "$YAML")"
check "no plaintext password is left behind" 0 "$(grep -c "$LOCAL_PW" "$YAML")"
check "nothing was written to disk" 0 "$([ -f "$KEY" ] && echo 1 || echo 0)"
check "the batch reports adoption, not a restore" 1 "$(grep -c 'Pull finished: 0 restored, 2 adopted' "$WORK_NATIVE/adopt.out")"
check "the 1Password dependency is spelled out" 2 "$(grep -c 'cannot connect while 1Password is unavailable' "$WORK_NATIVE/adopt.out")"

echo
echo "==> --from-vault --materialize is the off-ramp instead"
"$OPS" 1p pull --all --from-vault --materialize --yes > "$WORK_NATIVE/materialize.out" 2>&1
check "the key landed on disk" 1 "$([ -f "$KEY" ] && echo 1 || echo 0)"
check "it is the 1Password copy" "$KEY_WANT" "$(key_fpr "$KEY")"
check "key_path rebound to the file" 1 "$(grep -c 'key_path: ~/.ssh/opspulse_web' "$YAML")"
check "the password came back as plaintext" 1 "$(grep -c "password: $STUB_PW" "$YAML")"

echo
echo "==> an unmatched vault item is a hint, never an error"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
YAML
ORPHAN_RC=0
ORPHAN="$(STUB_OP_EXISTING=opspulse_web_key,opspulse_orphan_key "$OPS" 1p pull --all --from-vault --yes 2>&1)" || ORPHAN_RC=$?
check "the run still succeeds" 0 "$ORPHAN_RC"
check "the orphaned item is reported" 1 "$(printf '%s' "$ORPHAN" | grep -c 'opspulse_orphan_key')"
check "no server is invented for it" 1 "$(grep -c 'name: web' "$YAML")"
check "the orphan did not become a server" 0 "$(grep -c 'name: orphan' "$YAML")"

echo
if [ "$FAILURES" -eq 0 ]; then
	echo "OK: all assertions passed"
else
	echo "FAILED: $FAILURES assertion(s)"
	exit 1
fi
