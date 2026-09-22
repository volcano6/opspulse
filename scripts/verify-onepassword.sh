#!/usr/bin/env bash
#
# End-to-end verification for `ops 1p backup` / `ops 1p restore`.
#
# Why a stub instead of the real CLI: `op vault list` needs an interactive
# Desktop App approval, so a non-interactive harness can never drive the real
# binary. scripts/opstub answers the handful of subcommands OpsPulse uses, which is
# enough to pin the parts that actually break:
#
#   * a backup uploads every local key/password as a Login item (never an SSH
#     Key item, which the CLI silently discards) and the whole servers.yaml as
#     one Secure Note
#   * a backup never rewrites servers.yaml: local disk stays the source of truth
#   * a server still holding an op:// reference fails the backup before
#     anything reaches 1Password
#   * an existing item is refreshed through `item get` + `item edit`, not
#     re-created
#   * a write that does not read back verbatim fails the backup
#   * a restore that would write a plaintext password refuses in a pipe without
#     --yes, while a key-only restore never asks
#   * a foreign key on disk blocks the restore until --force says otherwise
#   * a no-argument restore bootstraps a fresh machine: the server list comes
#     back from the shared item, then every credential follows
#   * a restore migrates leftover op:// references to local credentials
#   * the inventory merge never deletes a server only this machine knows, and
#     refuses to guess at a real conflict in a pipe
#   * an opspulse_* item no server claims is reported as a hint, never an error
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
# Set here rather than where it is populated, so the trap below can run before
# the temp filesystem has been created.
OP_LOG_DIR=""

cleanup() {
	rm -rf "$WORK_POSIX"
	if [ -n "$OP_LOG_DIR" ]; then
		rm -rf "$OP_LOG_DIR"
	fi
}
trap cleanup EXIT
cleanup
# $WORK_POSIX/home is OPSPULSE_HOME (servers.yaml lives there); fakehome stands in
# for the user's home so that a restore never touches the real ~/.ssh.
mkdir -p "$WORK_POSIX/home" "$WORK_POSIX/fakehome/.ssh"

echo "==> building ops and the op stub"
# Build into the *native* form of the path: with MSYS path conversion disabled
# (MSYS_NO_PATHCONV=1) a /d/... target is handed to go.exe verbatim, which makes
# it resolve against the current drive and silently build somewhere else.
(cd "$REPO_POSIX" && go build -o "$WORK_NATIVE/ops$EXE" ./cmd/opspulse)
(cd "$REPO_POSIX" && go build -o "$WORK_NATIVE/opstub$EXE" ./scripts/opstub)
ssh-keygen -q -t ed25519 -N '' -f "$WORK_NATIVE/id_web" -C opspulse-verify
ssh-keygen -q -t ed25519 -N '' -f "$WORK_NATIVE/id_other" -C opspulse-other

export OPSPULSE_HOME="$WORK_NATIVE/home"
export OPSPULSE_OP_PATH="$WORK_NATIVE/opstub$EXE"
# The stub appends one line per invocation, and a backup now runs several op
# processes at once. O_APPEND is only atomic on filesystems that implement it
# properly, and WSL's DrvFs does not: 200 concurrent appends landed as 52 lines.
# A shared log next to the repo would therefore silently lose entries from the
# very record these assertions read, so it lives on a temp filesystem instead.
OP_LOG_DIR="$(mktemp -d)"
STUB_OP_LOG="$OP_LOG_DIR/op.log"
export STUB_OP_LOG
export STUB_OP_KEY="$WORK_NATIVE/id_web"
# Every backup and restore touches the shared inventory item, and the read-back
# check that guards it reads from this store. Leaving it unset would make the
# inventory read back empty and fail every run for the wrong reason.
NOTE_STORE="$WORK_NATIVE/note.store"
export STUB_OP_NOTE_STORE="$NOTE_STORE"
# a restore writes into ~/.ssh; redirect home so the real one is never touched
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

# KEY is where a restore writes a server's key: ~/.ssh/opspulse_<server>.
KEY="$WORK_POSIX/fakehome/.ssh/opspulse_web"
KEY_WANT="$(key_fpr "$WORK_NATIVE/id_web")"
FPR_OTHER="$(key_fpr "$WORK_NATIVE/id_other")"
if [ "$FPR_OTHER" = "$KEY_WANT" ]; then
	echo "  FAIL  the two key fixtures are not distinct"
	FAILURES=$((FAILURES + 1))
fi

echo
echo "==> backup uploads local credentials and leaves servers.yaml alone"
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
  - name: bare
    host: 10.0.0.12
    user: root
YAML
rm -f "$NOTE_STORE"
: > "$STUB_OP_LOG"
"$OPS" 1p backup > "$WORK_NATIVE/backup.out" 2>&1

echo
echo "==> op invocation log"
cat "$STUB_OP_LOG"

echo
echo "==> assertions"
check "servers.yaml is untouched: no op:// reference appears" 0 "$(grep -c 'op://' "$YAML")"
check "the plaintext password stays on local disk" 1 "$(grep -c "$LOCAL_PW" "$YAML")"
check "the local key path stays on local disk" 1 "$(grep -c "key_path: $WORK_NATIVE/id_web" "$YAML")"
check "the backup says servers.yaml was not rewritten" 1 "$(grep -c 'servers.yaml was left unchanged' "$WORK_NATIVE/backup.out")"
check "the backup reports two servers" 1 "$(grep -c 'Backed up credentials for 2 server' "$WORK_NATIVE/backup.out")"
check "a server with no credentials is skipped by name" 1 "$(grep -c 'Skipping "bare"' "$WORK_NATIVE/backup.out")"
check "each credential is a Login item" 2 "$(grep -c 'item template get Login' "$STUB_OP_LOG")"
check "the inventory is a Secure Note" 1 "$(grep -c 'item template get Secure Note' "$STUB_OP_LOG")"
# The real CLI accepts an SSH_KEY payload, exits 0, and stores nothing. The stub
# refuses it outright, so a regression back to SSH Key items fails here.
check "SSH Key templates are never requested" 0 "$(grep -c 'item template get SSH Key' "$STUB_OP_LOG")"
check "no payload targets the SSH_KEY category" 0 "$(grep -c 'category=SSH_KEY' "$STUB_OP_LOG")"
check "both credentials are Login items" 2 "$(grep -c 'category=LOGIN' "$STUB_OP_LOG")"
check "the inventory is a SECURE_NOTE payload" 1 "$(grep -c 'category=SECURE_NOTE' "$STUB_OP_LOG")"
check "every write arrives on stdin" 3 "$(grep -c 'item create --vault Personal -' "$STUB_OP_LOG")"
check "every write received a non-empty stdin" 3 "$(grep -c 'item create --vault Personal - | stdin=[1-9]' "$STUB_OP_LOG")"
check "web's key was uploaded as a Login item" 1 "$(grep -c 'item=opspulse_web_key | category=LOGIN' "$STUB_OP_LOG")"
# Every op invocation is a full round trip through the Desktop App, so the vault
# is listed once for the whole run rather than once per credential. Listing it
# per credential was the difference between 3 and 5 round trips here, and between
# roughly 49 and 17 on a 13-server inventory.
check "the vault is listed once, not once per credential" 1 "$(grep -c 'item list --vault Personal --format json' "$STUB_OP_LOG")"
check "the inventory carries web's definition" 1 "$(grep -c 'name: web' "$NOTE_STORE")"
check "the inventory carries the whole server list" 1 "$(grep -c 'name: vps_01' "$NOTE_STORE")"

echo
echo "==> status is offline and reports local credentials"
: > "$STUB_OP_LOG"
"$OPS" 1p status > "$WORK_NATIVE/status.out" 2>&1
check "status contacts no 1Password at all" 0 "$(wc -l < "$STUB_OP_LOG" | tr -d ' ')"
check "status sees every server as local" 1 "$(grep -c 'All 3 server(s) resolve their credentials from local disk' "$WORK_NATIVE/status.out")"

echo
echo "==> status flags a server whose key file is not on this machine"
# servers.yaml can name a key that no longer exists: a reinstall, a rename, or a
# careless rm in ~/.ssh. Without this check status reports a green "all local"
# and the problem only surfaces when the connection dies.
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
  - name: gone
    host: 10.0.0.13
    user: root
    key_path: $WORK_NATIVE/no-such-key
YAML
"$OPS" 1p status > "$WORK_NATIVE/status-missing.out" 2>&1
check "the missing key file is named" 1 "$(grep -c 'point at a private key file that is not on this machine: gone' "$WORK_NATIVE/status-missing.out")"
check "the all-local reassurance is withheld" 0 "$(grep -c 'resolve their credentials from local disk' "$WORK_NATIVE/status-missing.out")"

echo
echo "==> a parallel backup uploads every server, not just the ones that fit"
cat > "$YAML" <<YAML
servers:
  - name: p1
    host: 10.0.0.101
    user: root
    key_path: $WORK_NATIVE/id_web
  - name: p2
    host: 10.0.0.102
    user: root
    key_path: $WORK_NATIVE/id_web
  - name: p3
    host: 10.0.0.103
    user: root
    password: $LOCAL_PW
  - name: p4
    host: 10.0.0.104
    user: root
    password: $LOCAL_PW
  - name: p5
    host: 10.0.0.105
    user: root
    key_path: $WORK_NATIVE/id_web
  - name: p6
    host: 10.0.0.106
    user: root
    password: $LOCAL_PW
YAML
rm -f "$NOTE_STORE"
: > "$STUB_OP_LOG"
PARALLEL_OUT="$("$OPS" 1p backup -p 3 2>&1)"
check "every server is counted" 1 "$(printf '%s' "$PARALLEL_OUT" | grep -c 'Backed up credentials for 6 server')"
check "no server failed" 0 "$(printf '%s' "$PARALLEL_OUT" | grep -c '❌')"
check "each key reached the vault" 3 "$(grep -c 'item=opspulse_p[0-9]_key | category=LOGIN' "$STUB_OP_LOG")"
check "each password reached the vault" 3 "$(grep -c 'item=opspulse_p[0-9]_password | category=LOGIN' "$STUB_OP_LOG")"
# The whole point of the change: a concurrent run must not turn into one listing
# per server, which is what made a 13-server backup take minutes.
check "the vault is listed once for the whole batch" 1 "$(grep -c 'item list --vault Personal --format json' "$STUB_OP_LOG")"
check "the parallel run still leaves servers.yaml alone" 0 "$(grep -c 'op://' "$YAML")"
check "the parallel run reports no plaintext password change" 3 "$(grep -c "password: $LOCAL_PW" "$YAML")"

echo
echo "==> a leftover op:// reference fails the backup before 1Password is touched"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: op://Personal/opspulse_web_key/opspulse_private_key
YAML
: > "$STUB_OP_LOG"
LEGACY_RC=0
LEGACY="$(STUB_OP_EXISTING= "$OPS" 1p backup 2>&1)" || LEGACY_RC=$?
check "the legacy reference fails the backup" 1 "$LEGACY_RC"
check "the failure points at ops 1p restore" 1 "$(printf '%s' "$LEGACY" | grep -c 'ops 1p restore')"
check "1Password was never invoked" 0 "$(wc -l < "$STUB_OP_LOG" | tr -d ' ')"
check "servers.yaml still holds the reference" 1 "$(grep -c 'op://' "$YAML")"

echo
echo "==> an existing item is refreshed, not re-created"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
YAML
rm -f "$NOTE_STORE"
: > "$STUB_OP_LOG"
STUB_OP_EXISTING=opspulse_web_key,opspulse_inventory "$OPS" 1p backup >/dev/null 2>&1
check "the existing item is read before the update" 1 "$(grep -c 'item get item-opspulse_web_key --vault Personal --format json' "$STUB_OP_LOG")"
check "the update arrives on stdin" 1 "$(grep -c 'item edit item-opspulse_web_key --vault Personal | stdin=[1-9]' "$STUB_OP_LOG")"
check "nothing is re-created" 0 "$(grep -c 'item create' "$STUB_OP_LOG")"

echo
echo "==> a key write that does not read back verbatim fails the backup"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
YAML
rm -f "$NOTE_STORE"
MISMATCH_RC=0
MISMATCH="$(STUB_OP_READ_KEY="$WORK_NATIVE/id_other" "$OPS" 1p backup 2>&1)" || MISMATCH_RC=$?
check "the mismatch fails the backup" 1 "$MISMATCH_RC"
check "the failure explains itself" 1 "$(printf '%s' "$MISMATCH" | grep -c 'could not read it back')"
check "servers.yaml was left unchanged" 1 "$(grep -c "key_path: $WORK_NATIVE/id_web" "$YAML")"
check "no op:// reference was written" 0 "$(grep -c 'op://' "$YAML")"

echo
echo "==> a restore that would write a password refuses in a pipe"
cat > "$YAML" <<YAML
servers:
  - name: vps_01
    host: 10.0.0.11
    user: root
YAML
# stdin is a pipe on purpose. Redirecting from /dev/null would not do: /dev/null
# is a character device, so stdinIsInteractive() would report a terminal and the
# confirmation prompt would block a non-interactive run.
PIPED="$(printf '' | STUB_OP_EXISTING=opspulse_vps_01_password "$OPS" 1p restore vps_01 2>&1 || true)"
check "the piped restore points at --yes" 1 "$(printf '%s' "$PIPED" | grep -c -- '--yes')"
check "the piped restore wrote nothing" 0 "$(grep -c "$STUB_PW" "$YAML")"

echo
echo "==> restore writes a password back into servers.yaml (--yes)"
STUB_OP_EXISTING=opspulse_vps_01_password "$OPS" 1p restore vps_01 --yes > "$WORK_NATIVE/restore-pw.out" 2>&1
check "the plaintext password is restored" 1 "$(grep -c "password: $STUB_PW" "$YAML")"
check "the restore warns what it wrote" 1 "$(grep -c 'Wrote 1 plaintext password' "$WORK_NATIVE/restore-pw.out")"
check "the summary counts one restore" 1 "$(grep -c 'Restore finished: 1 restored, 0 skipped, 0 blocked, 0 failed' "$WORK_NATIVE/restore-pw.out")"

echo
echo "==> a key-only restore needs no confirmation"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: ~/.ssh/opspulse_web
YAML
rm -f "$KEY" "$KEY.pub"
STUB_OP_EXISTING=opspulse_web_key "$OPS" 1p restore web >/dev/null 2>&1
check "the private key was written" 1 "$([ -f "$KEY" ] && echo 1 || echo 0)"
check "the .pub sibling was derived" 1 "$([ -f "$KEY.pub" ] && echo 1 || echo 0)"
check "the key is parseable" 0 "$(ssh-keygen -l -f "$KEY" >/dev/null 2>&1; echo $?)"
check "the key on disk is the 1Password copy" "$KEY_WANT" "$(key_fpr "$KEY")"
check "key_path stays bound to the file" 1 "$(grep -c 'key_path: ~/.ssh/opspulse_web' "$YAML")"

echo
echo "==> a different key already on disk is not overwritten without --force"
# Only the private key is swapped; the stale .pub from the previous restore stays
# behind on purpose, so that a check which consults it would be caught.
cp "$WORK_NATIVE/id_other" "$KEY"
CONFLICT_RC=0
CONFLICT="$(STUB_OP_EXISTING=opspulse_web_key "$OPS" 1p restore web 2>&1)" || CONFLICT_RC=$?
check "the conflict is reported with a way out" 1 "$(printf '%s' "$CONFLICT" | grep -c -- '--force')"
check "a blocked restore exits non-zero" 1 "$CONFLICT_RC"
check "the foreign key survived untouched" "$FPR_OTHER" "$(key_fpr "$KEY")"

STUB_OP_EXISTING=opspulse_web_key "$OPS" 1p restore web --force >/dev/null 2>&1
check "--force replaces it with the 1Password copy" "$KEY_WANT" "$(key_fpr "$KEY")"

echo
echo "==> a leftover op:// reference is migrated to a local credential"
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
YAML
rm -f "$KEY" "$KEY.pub"
MIGRATE="$(STUB_OP_EXISTING=opspulse_web_key,opspulse_vps_01_password "$OPS" 1p restore --yes 2>&1)"
check "the key was migrated to disk" 1 "$([ -f "$KEY" ] && echo 1 || echo 0)"
check "the password was migrated to plaintext" 1 "$(grep -c "password: $STUB_PW" "$YAML")"
check "no op:// reference is stranded" 0 "$(grep -c 'op://' "$YAML")"
check "the migration is called out" 1 "$(printf '%s' "$MIGRATE" | grep -c 'Migrated 2 server')"

# The fresh-machine scenarios need their own home; they run after the shared
# $YAML so that nothing after them depends on its state.
HOME2="$WORK_NATIVE/home2"

echo
echo "==> a no-argument restore bootstraps a machine that has nothing"
rm -rf "$HOME2"
mkdir -p "$HOME2"
rm -f "$KEY" "$KEY.pub" "$WORK_POSIX/fakehome/.ssh/opspulse_vps1"
cat > "$NOTE_STORE" <<YAML
servers:
    - name: vps1
      host: 10.0.0.21
      port: 22
      user: root
      key_path: ~/.ssh/opspulse_vps1
    - name: vps2
      host: 10.0.0.22
      port: 22
      user: root
      password: restored-pw
YAML
BOOTSTRAP_RC=0
STUB_OP_EXISTING=opspulse_inventory,opspulse_vps1_key \
	OPSPULSE_HOME="$HOME2" "$OPS" 1p restore > "$WORK_NATIVE/bootstrap.out" 2>&1 || BOOTSTRAP_RC=$?
check "the bootstrap succeeds" 0 "$BOOTSTRAP_RC"
check "the server list came back" 1 "$(grep -c 'name: vps1' "$HOME2/servers.yaml")"
check "the second server came back too" 1 "$(grep -c 'name: vps2' "$HOME2/servers.yaml")"
check "the restore reports the list" 1 "$(grep -c 'Restored the server list' "$WORK_NATIVE/bootstrap.out")"
check "the key followed onto disk" 1 "$([ -f "$WORK_POSIX/fakehome/.ssh/opspulse_vps1" ] && echo 1 || echo 0)"
check "the summary counts one restore" 1 "$(grep -c 'Restore finished: 1 restored, 1 skipped, 0 blocked, 0 failed' "$WORK_NATIVE/bootstrap.out")"

echo
echo "==> a restore never deletes a server only this machine knows"
cat > "$HOME2/servers.yaml" <<YAML
servers:
  - name: localonly
    host: 10.0.0.31
    user: root
YAML
MERGE_RC=0
STUB_OP_EXISTING=opspulse_inventory,opspulse_vps1_key \
	OPSPULSE_HOME="$HOME2" "$OPS" 1p restore > "$WORK_NATIVE/merge.out" 2>&1 || MERGE_RC=$?
check "the merge succeeds" 0 "$MERGE_RC"
check "the local-only server survived" 1 "$(grep -c 'name: localonly' "$HOME2/servers.yaml")"
check "the vault servers were restored" 1 "$(grep -c 'name: vps1' "$HOME2/servers.yaml")"
check "the restore reports what it kept" 1 "$(grep -c 'only this machine had' "$WORK_NATIVE/merge.out")"

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
ORPHAN="$(STUB_OP_EXISTING=opspulse_web_key,opspulse_orphan_key "$OPS" 1p restore web 2>&1)" || ORPHAN_RC=$?
check "the run still succeeds" 0 "$ORPHAN_RC"
check "the orphaned item is reported" 1 "$(printf '%s' "$ORPHAN" | grep -c 'opspulse_orphan_key')"
check "the orphan did not become a server" 0 "$(grep -c 'name: orphan' "$YAML")"

echo
echo "==> a backup unions the inventory, so another machine's servers survive"
cat > "$NOTE_STORE" <<YAML
servers:
    - name: vps1
      host: 10.0.0.21
      port: 22
      user: root
YAML
cat > "$YAML" <<YAML
servers:
  - name: vps3
    host: 10.0.0.23
    user: root
YAML
STUB_OP_EXISTING=opspulse_inventory "$OPS" 1p backup > "$WORK_NATIVE/union.out" 2>&1
check "the vault server survived the backup" 1 "$(grep -c 'name: vps1' "$NOTE_STORE")"
check "the local-only server was added" 1 "$(grep -c 'name: vps3' "$NOTE_STORE")"
check "the union is reported" 1 "$(grep -c 'were not in the backup yet' "$WORK_NATIVE/union.out")"

echo
echo "==> a backup that is merely behind is still refreshed"
# The local file already holds everything in the backup plus one more server.
# Neither counter moves in this case - the extra server was never "added" from
# the backup's point of view, and nothing was "updated" - so a skip decided from
# the counters alone would leave the backup permanently one server behind. This
# is the shape a real machine has after adding a server.
cat > "$NOTE_STORE" <<YAML
servers:
    - name: vps1
      host: 10.0.0.21
      port: 22
      user: root
    - name: vps2
      host: 10.0.0.22
      port: 22
      user: root
    - name: vps3
      host: 10.0.0.23
      port: 22
      user: root
YAML
cp "$NOTE_STORE" "$YAML"
cat >> "$YAML" <<YAML
    - name: vps4
      host: 10.0.0.24
      port: 22
      user: root
YAML
SUPERSET="$(STUB_OP_EXISTING=opspulse_inventory "$OPS" 1p backup 2>&1)"
check "the backup is not mistaken for up to date" 1 "$(printf '%s' "$SUPERSET" | grep -c 'Backed up the server list (4 server')"
check "the missing server reached the backup" 1 "$(grep -c 'name: vps4' "$NOTE_STORE")"
check "the shortfall is reported" 1 "$(printf '%s' "$SUPERSET" | grep -c 'were not in the backup yet')"

echo
echo "==> a real conflict is refused in a pipe, and --prefer-remote settles it"
cat > "$NOTE_STORE" <<YAML
servers:
    - name: vps1
      host: 10.0.0.21
      port: 22
      user: root
YAML
cat > "$YAML" <<YAML
servers:
  - name: vps1
    host: 10.0.0.99
    user: root
YAML
CONFLICT_INV_RC=0
CONFLICT_INV="$(printf '' | STUB_OP_EXISTING=opspulse_inventory "$OPS" 1p backup 2>&1)" || CONFLICT_INV_RC=$?
check "an undecided conflict fails" 1 "$CONFLICT_INV_RC"
check "the conflict shows the field-level diff" 1 "$(printf '%s' "$CONFLICT_INV" | grep -c 'host: 10.0.0.99 -> 10.0.0.21')"
check "the conflict offers a way out" 1 "$(printf '%s' "$CONFLICT_INV" | grep -c -- '--prefer-local')"
check "the backup was not written" 1 "$(grep -c 'host: 10.0.0.21' "$NOTE_STORE")"
check "the local host was not written" 0 "$(grep -c '10.0.0.99' "$NOTE_STORE")"

STUB_OP_EXISTING=opspulse_inventory "$OPS" 1p backup --prefer-remote >/dev/null 2>&1
check "--prefer-remote writes the vault's definition" 1 "$(grep -c 'host: 10.0.0.21' "$NOTE_STORE")"

echo
echo "==> an inventory write that does not read back verbatim is caught"
# The real CLI has accepted a write, exited 0, and stored something else. The
# stub reproduces the observable half by serving a different note on read than
# the one it just accepted. The override must differ from the local file,
# otherwise the merge finds nothing to do and never writes at all.
cat > "$YAML" <<YAML
servers:
  - name: vps9
    host: 10.0.0.99
    user: root
YAML
MISMATCH_INV_RC=0
MISMATCH_INV="$(STUB_OP_EXISTING=opspulse_inventory \
	STUB_OP_NOTE_READ=$'servers:\n  - name: phantom\n    host: 10.0.0.50\n    user: root\n' \
	"$OPS" 1p backup 2>&1)" || MISMATCH_INV_RC=$?
check "the mismatch fails the backup" 1 "$MISMATCH_INV_RC"
check "the mismatch explains itself" 1 "$(printf '%s' "$MISMATCH_INV" | grep -c 'did not store the inventory verbatim')"

echo
echo "==> the retired push/pull names point at the new commands"
PUSH_OUT="$("$OPS" 1p push 2>&1 || true)"
PULL_OUT="$("$OPS" 1p pull 2>&1 || true)"
check "push is retired in favour of backup" 1 "$(printf '%s' "$PUSH_OUT" | grep -c "use 'ops 1p backup'")"
check "pull is retired in favour of restore" 1 "$(printf '%s' "$PULL_OUT" | grep -c "use 'ops 1p restore'")"

echo
if [ "$FAILURES" -eq 0 ]; then
	echo "OK: all assertions passed"
else
	echo "FAILED: $FAILURES assertion(s)"
	exit 1
fi
