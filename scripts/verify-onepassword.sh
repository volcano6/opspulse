#!/usr/bin/env bash
#
# End-to-end verification for `ops 1p backup` / `ops 1p restore`.
#
# Why a stub instead of the real CLI: `op vault list` needs an interactive
# Desktop App approval, so a non-interactive harness can never drive the real
# binary. scripts/opstub answers the handful of subcommands OpsPulse uses, which is
# enough to pin the parts that actually break:
#
#   * a backup puts the whole servers.yaml and every local private key into ONE
#     Secure Note named after this machine, and costs two op calls rather than
#     one per credential
#   * a backup never rewrites servers.yaml: local disk stays the source of truth
#   * a server still holding an op:// reference fails the backup before
#     anything reaches 1Password
#   * an existing backup document is refreshed through `item edit` alone, with
#     no `item get` first and no `item create`
#   * a first backup falls back to `item create` only on the CLI's own
#     "could not find item" answer
#   * a write that does not read back verbatim fails the backup
#   * a restore reads the backup document and writes the keys it carries to
#     ~/.ssh, without a single per-credential op call
#   * a restore that would write a plaintext password refuses in a pipe without
#     --yes, while a key-only restore never asks
#   * a foreign key on disk blocks the restore until --force says otherwise
#   * a no-argument restore bootstraps a fresh machine: the server list comes
#     back from the backup document, then every credential follows
#   * a restore migrates leftover op:// references to local credentials, which
#     is the fallback for a vault that only holds the old per-server items
#   * the inventory merge never deletes a server only this machine knows
#   * a backup document no server claims is not reported as an orphan, while a
#     genuinely orphaned opspulse_* item still is
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
# A backup is now two op calls, but the log is still the record every assertion
# below reads, so it lives on a temp filesystem rather than next to the repo.
OP_LOG_DIR="$(mktemp -d)"
STUB_OP_LOG="$OP_LOG_DIR/op.log"
export STUB_OP_LOG
export STUB_OP_KEY="$WORK_NATIVE/id_web"
# The backup document is a Secure Note, and the read-back check that guards it
# reads from this store. Leaving it unset would make the read-back come up empty
# and fail every run for the wrong reason.
NOTE_STORE="$WORK_NATIVE/note.store"
export STUB_OP_NOTE_STORE="$NOTE_STORE"
# a restore writes into ~/.ssh; redirect home so the real one is never touched
export USERPROFILE="$WORK_NATIVE/fakehome"
export HOME="$WORK_NATIVE/fakehome"
mkdir -p "$WORK_POSIX/fakehome/.ssh"

OPS="$WORK_NATIVE/ops$EXE"
YAML="$WORK_POSIX/home/servers.yaml"
LOCAL_PW="local-plaintext-pw"
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

# blob_title pulls this machine's item title out of a backup's own report, so
# that the harness never has to guess what os.Hostname() returns on the machine
# running it. Every later `STUB_OP_EXISTING` that has to look like "the backup
# document is already in the vault" is built from this.
blob_title() {
	printf '%s' "$1" | sed -n 's/.*into "\([^"]*\)" in vault.*/\1/p' | head -1
}

# seed_blob backs up whatever servers.yaml currently holds, leaving the note
# store with a real backup document for the restore scenarios that follow. It is
# how those scenarios get their fixture without hand-writing YAML that embeds a
# private key.
seed_blob() {
	rm -f "$NOTE_STORE"
	STUB_OP_EXISTING= "$OPS" 1p backup --vault Personal >/dev/null 2>&1
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
echo "==> a first backup stores the whole inventory in one document"
# No --vault and nothing remembered, so this is the worst case: the vault has to
# be listed first. It is the one run that costs a call the steady state does not.
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
check "the backup reports every server, credential or not" 1 "$(grep -c 'Backing up 3 server(s) and 1 private key(s)' "$WORK_NATIVE/backup.out")"
check "the completion line names the item and the vault" 1 "$(grep -c 'Backed up 3 server(s) to "opspulse_inventory_' "$WORK_NATIVE/backup.out")"
check "the vault is listed once for the whole run" 1 "$(grep -c 'vault list --format json' "$STUB_OP_LOG")"
# edit-first: the document does not exist yet, so the attempt fails with the
# CLI's own "could not find item" and only then is a create issued. This is the
# one shape a real vault would reject a duplicate for.
check "the write is attempted as an edit first" 1 "$(grep -c 'item edit opspulse_inventory_' "$STUB_OP_LOG")"
check "the create fallback runs exactly once" 1 "$(grep -c 'item create --vault Personal -' "$STUB_OP_LOG")"
check "the create received a non-empty stdin" 1 "$(grep -c 'item create --vault Personal - | stdin=[1-9]' "$STUB_OP_LOG")"
check "the document is read back once" 1 "$(grep -c 'op read op://Personal/opspulse_inventory_.*/notesPlain' "$STUB_OP_LOG")"
check "a first backup costs four calls (list, edit, create, read)" 4 "$(wc -l < "$STUB_OP_LOG" | tr -d ' ')"
# Nothing fetches a template any more: the document is built locally, which is
# what keeps a backup to a single write. A regression here would add a call back.
check "no item template is ever fetched" 0 "$(grep -c 'item template get' "$STUB_OP_LOG")"
check "no item is read with item get" 0 "$(grep -c 'item get ' "$STUB_OP_LOG")"
# The real CLI accepts an SSH_KEY payload, exits 0, and stores nothing. The stub
# refuses it outright, so a regression back to SSH Key items fails here.
check "no payload targets the SSH_KEY category" 0 "$(grep -c 'category=SSH_KEY' "$STUB_OP_LOG")"
check "every payload written is a SECURE_NOTE document" 2 "$(grep -c 'category=SECURE_NOTE' "$STUB_OP_LOG")"
check "the document is versioned" 1 "$(grep -c '^version: 1$' "$NOTE_STORE")"
check "the document names the machine" 1 "$(grep -c '^machine: ' "$NOTE_STORE")"
check "the document carries the whole server list" 1 "$(grep -c 'name: vps_01' "$NOTE_STORE")"
check "a credential-less server travels too" 1 "$(grep -c 'name: bare' "$NOTE_STORE")"
check "the document carries the private key" 1 "$(grep -c 'BEGIN OPENSSH PRIVATE KEY' "$NOTE_STORE")"

BLOB_TITLE="$(blob_title "$(cat "$WORK_NATIVE/backup.out")")"
case "$BLOB_TITLE" in
opspulse_inventory_*)
	check "the backup item is named after this machine" 1 1
	;;
*)
	check "the backup item is named after this machine" "opspulse_inventory_*" "$BLOB_TITLE"
	;;
esac

echo
echo "==> a repeat backup is two calls: one edit, one read-back"
# This is the change in one assertion. The old per-credential design spent one
# call per key and one per password, plus a listing; on a large fleet that ran
# to dozens of calls and over two minutes. Here the whole inventory is refreshed
# by editing the document in place, which needs no listing and no
# read-before-write.
#
# No --vault is passed, which is the point: the vault the first backup had to
# discover is remembered, so the steady state never lists vaults again. Paying
# for that listing on every run would make this three calls, not two.
: > "$STUB_OP_LOG"
STUB_OP_EXISTING="$BLOB_TITLE" "$OPS" 1p backup > "$WORK_NATIVE/backup2.out" 2>&1
check "the whole backup costs two calls" 2 "$(wc -l < "$STUB_OP_LOG" | tr -d ' ')"
check "the document is updated in place" 1 "$(grep -c 'item edit opspulse_inventory_.* --vault Personal | stdin=[1-9]' "$STUB_OP_LOG")"
check "nothing is re-created" 0 "$(grep -c 'item create' "$STUB_OP_LOG")"
check "the discovered vault was remembered, so nothing is listed" 0 "$(grep -c 'vault list' "$STUB_OP_LOG")"
check "the update is verified by reading it back" 1 "$(grep -c 'op read op://Personal/opspulse_inventory_.*/notesPlain' "$STUB_OP_LOG")"

echo
echo "==> a key file that cannot be read is skipped, not fatal"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/no-such-key
  - name: vps_01
    host: 10.0.0.11
    user: root
    password: $LOCAL_PW
YAML
rm -f "$NOTE_STORE"
MISSING_KEY_OUT="$(STUB_OP_EXISTING= "$OPS" 1p backup --vault Personal 2>&1)"
check "the missing key file is reported" 1 "$(printf '%s' "$MISSING_KEY_OUT" | grep -c 'cannot read')"
check "the backup still reports one server" 1 "$(printf '%s' "$MISSING_KEY_OUT" | grep -c 'Backing up 2 server(s) and 0 private key(s)')"
check "the backup still succeeds" 1 "$(printf '%s' "$MISSING_KEY_OUT" | grep -c 'verified byte for byte')"
check "the server list still travelled" 1 "$(grep -c 'name: web' "$NOTE_STORE")"

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
LEGACY="$(STUB_OP_EXISTING= "$OPS" 1p backup --vault Personal 2>&1)" || LEGACY_RC=$?
check "the legacy reference fails the backup" 1 "$LEGACY_RC"
check "the failure points at ops 1p restore" 1 "$(printf '%s' "$LEGACY" | grep -c 'ops 1p restore')"
check "1Password was never invoked" 0 "$(wc -l < "$STUB_OP_LOG" | tr -d ' ')"
check "servers.yaml still holds the reference" 1 "$(grep -c 'op://' "$YAML")"

echo
echo "==> a write that does not read back verbatim fails the backup"
# The real CLI has accepted a write, exited 0, and stored something else. The
# stub reproduces the observable half by serving a different body on read than
# the one it just accepted.
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
YAML
rm -f "$NOTE_STORE"
MISMATCH_RC=0
MISMATCH="$(STUB_OP_EXISTING="$BLOB_TITLE" \
	STUB_OP_NOTE_READ=$'version: 1\nservers:\n  - name: phantom\n    host: 10.0.0.50\n' \
	"$OPS" 1p backup --vault Personal 2>&1)" || MISMATCH_RC=$?
check "the mismatch fails the backup" 1 "$MISMATCH_RC"
check "the failure explains itself" 1 "$(printf '%s' "$MISMATCH" | grep -c 'did not store the backup verbatim')"
check "servers.yaml was left unchanged" 1 "$(grep -c "key_path: $WORK_NATIVE/id_web" "$YAML")"
check "no op:// reference was written" 0 "$(grep -c 'op://' "$YAML")"

echo
echo "==> status is offline and reports local credentials"
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
: > "$STUB_OP_LOG"
"$OPS" 1p status > "$WORK_NATIVE/status.out" 2>&1
check "status contacts no 1Password at all" 0 "$(wc -l < "$STUB_OP_LOG" | tr -d ' ')"
check "status sees every server as local" 1 "$(grep -c 'All 3 server(s) resolve their credentials from local disk' "$WORK_NATIVE/status.out")"

echo
echo "==> status --remote sees a server that only the backup document holds"
# The per-server items are historical leftovers. Without reading the documents,
# every server backed up since the switch would be reported as never backed up.
seed_blob
: > "$STUB_OP_LOG"
STUB_OP_EXISTING="$BLOB_TITLE" "$OPS" 1p status --remote > "$WORK_NATIVE/status-remote.out" 2>&1
check "every server is matched to the backup document" 3 "$(grep -c "✅ $BLOB_TITLE" "$WORK_NATIVE/status-remote.out")"
check "no server is reported as never backed up" 0 "$(grep -c 'not backed up' "$WORK_NATIVE/status-remote.out")"

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

# The restore scenarios read the backup document seeded above, which carries
# web's key and vps_01's password. They rewrite servers.yaml to the state a new
# machine would be in, so the credentials really do come from the document.
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
PIPED="$(printf '' | STUB_OP_EXISTING="$BLOB_TITLE" "$OPS" 1p restore vps_01 2>&1 || true)"
check "the piped restore points at --yes" 1 "$(printf '%s' "$PIPED" | grep -c -- '--yes')"
check "the piped restore wrote nothing" 0 "$(grep -c "$LOCAL_PW" "$YAML")"

echo
echo "==> restore writes a password back into servers.yaml (--yes)"
STUB_OP_EXISTING="$BLOB_TITLE" "$OPS" 1p restore vps_01 --yes > "$WORK_NATIVE/restore-pw.out" 2>&1
check "the password came out of the backup document" 1 "$(grep -c 'from the 1Password backup document' "$WORK_NATIVE/restore-pw.out")"
check "the plaintext password is restored" 1 "$(grep -c "password: $LOCAL_PW" "$YAML")"
check "the restore warns what it wrote" 1 "$(grep -c 'Wrote 1 plaintext password' "$WORK_NATIVE/restore-pw.out")"
check "the summary counts one restore" 1 "$(grep -c 'Restore finished: 1 restored, 0 skipped, 0 blocked, 0 failed' "$WORK_NATIVE/restore-pw.out")"

echo
echo "==> a key-only restore needs no confirmation and no per-credential call"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: ~/.ssh/opspulse_web
YAML
rm -f "$KEY" "$KEY.pub"
: > "$STUB_OP_LOG"
STUB_OP_EXISTING="$BLOB_TITLE" "$OPS" 1p restore web > "$WORK_NATIVE/restore-key.out" 2>&1
check "the private key was written" 1 "$([ -f "$KEY" ] && echo 1 || echo 0)"
check "the .pub sibling was derived" 1 "$([ -f "$KEY.pub" ] && echo 1 || echo 0)"
check "the key is parseable" 0 "$(ssh-keygen -l -f "$KEY" >/dev/null 2>&1; echo $?)"
check "the key on disk is the backed-up copy" "$KEY_WANT" "$(key_fpr "$KEY")"
check "key_path stays bound to the file" 1 "$(grep -c 'key_path: ~/.ssh/opspulse_web' "$YAML")"
# The key came out of the document already in memory, so no credential was read
# from the vault at all - only the document itself was.
check "no per-server item was read" 0 "$(grep -c 'op read op://Personal/opspulse_web_key' "$STUB_OP_LOG")"
check "only the backup document was read" 1 "$(grep -c 'op read op://Personal/opspulse_inventory_.*/notesPlain' "$STUB_OP_LOG")"

echo
echo "==> a different key already on disk is not overwritten without --force"
# Only the private key is swapped; the stale .pub from the previous restore stays
# behind on purpose, so that a check which consults it would be caught.
cp "$WORK_NATIVE/id_other" "$KEY"
CONFLICT_RC=0
CONFLICT="$(STUB_OP_EXISTING="$BLOB_TITLE" "$OPS" 1p restore web 2>&1)" || CONFLICT_RC=$?
check "the conflict is reported with a way out" 1 "$(printf '%s' "$CONFLICT" | grep -c -- '--force')"
check "a blocked restore exits non-zero" 1 "$CONFLICT_RC"
check "the foreign key survived untouched" "$FPR_OTHER" "$(key_fpr "$KEY")"

STUB_OP_EXISTING="$BLOB_TITLE" "$OPS" 1p restore web --force >/dev/null 2>&1
check "--force replaces it with the backed-up copy" "$KEY_WANT" "$(key_fpr "$KEY")"

echo
echo "==> a leftover op:// reference is migrated from the per-server items"
# The fallback for a vault that only holds the old format: no backup document is
# listed, so the per-server items are what the restore matches against.
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
check "the password was migrated to plaintext" 1 "$(grep -c "password: stub-vault-pw" "$YAML")"
check "no op:// reference is stranded" 0 "$(grep -c 'op://' "$YAML")"
check "the migration is called out" 1 "$(printf '%s' "$MIGRATE" | grep -c 'Migrated 2 server')"

# The fresh-machine scenarios need their own home; they run after the shared
# $YAML so that nothing after them depends on its state.
HOME2="$WORK_NATIVE/home2"

echo
echo "==> a no-argument restore bootstraps a machine that has nothing"
# The fixture is a real backup document, produced by backing up a machine that
# had both a key and a password. Building it this way is what makes this a
# round-trip test rather than a hand-written YAML the parser might disagree with.
cat > "$YAML" <<YAML
servers:
  - name: vps1
    host: 10.0.0.21
    user: root
    key_path: $WORK_NATIVE/id_web
  - name: vps2
    host: 10.0.0.22
    user: root
    password: restored-pw
YAML
seed_blob
rm -rf "$HOME2"
mkdir -p "$HOME2"
rm -f "$KEY" "$KEY.pub" "$WORK_POSIX/fakehome/.ssh/opspulse_vps1"
BOOTSTRAP_RC=0
STUB_OP_EXISTING="$BLOB_TITLE" \
	OPSPULSE_HOME="$HOME2" "$OPS" 1p restore > "$WORK_NATIVE/bootstrap.out" 2>&1 || BOOTSTRAP_RC=$?
check "the bootstrap succeeds" 0 "$BOOTSTRAP_RC"
check "the server list came back" 1 "$(grep -c 'name: vps1' "$HOME2/servers.yaml")"
check "the second server came back too" 1 "$(grep -c 'name: vps2' "$HOME2/servers.yaml")"
check "the restore reports the list" 1 "$(grep -c 'Restored the server list' "$WORK_NATIVE/bootstrap.out")"
check "the key followed onto disk" 1 "$([ -f "$WORK_POSIX/fakehome/.ssh/opspulse_vps1" ] && echo 1 || echo 0)"
check "the key on disk is the backed-up copy" "$KEY_WANT" "$(key_fpr "$WORK_POSIX/fakehome/.ssh/opspulse_vps1")"
check "the summary counts one restore" 1 "$(grep -c 'Restore finished: 1 restored, 1 skipped, 0 blocked, 0 failed' "$WORK_NATIVE/bootstrap.out")"
# The file was empty, so nothing here predates the backup: reporting a "kept"
# count would be arithmetic on the backup's size rather than on what was local.
check "a fresh machine keeps nothing, so it says nothing" 0 "$(grep -c 'only this machine had' "$WORK_NATIVE/bootstrap.out")"

echo
echo "==> a restore never deletes a server only this machine knows"
cat > "$HOME2/servers.yaml" <<YAML
servers:
  - name: localonly
    host: 10.0.0.31
    user: root
YAML
MERGE_RC=0
STUB_OP_EXISTING="$BLOB_TITLE" \
	OPSPULSE_HOME="$HOME2" "$OPS" 1p restore > "$WORK_NATIVE/merge.out" 2>&1 || MERGE_RC=$?
check "the merge succeeds" 0 "$MERGE_RC"
check "the local-only server survived" 1 "$(grep -c 'name: localonly' "$HOME2/servers.yaml")"
check "the vault servers were restored" 1 "$(grep -c 'name: vps1' "$HOME2/servers.yaml")"
check "the restore reports what it kept" 1 "$(grep -c 'Kept 1 server(s) that only this machine had' "$WORK_NATIVE/merge.out")"

echo
echo "==> an orphaned item is a hint, but a backup document is not an orphan"
cat > "$YAML" <<YAML
servers:
  - name: web
    host: 10.0.0.10
    user: ubuntu
    key_path: $WORK_NATIVE/id_web
YAML
ORPHAN_RC=0
ORPHAN="$(STUB_OP_EXISTING="$BLOB_TITLE",opspulse_web_key,opspulse_orphan_key "$OPS" 1p restore web 2>&1)" || ORPHAN_RC=$?
check "the run still succeeds" 0 "$ORPHAN_RC"
check "the orphaned item is reported" 1 "$(printf '%s' "$ORPHAN" | grep -c 'opspulse_orphan_key')"
check "the backup document is not called an orphan" 0 "$(printf '%s' "$ORPHAN" | grep -c "$BLOB_TITLE")"
check "the orphan did not become a server" 0 "$(grep -c 'name: orphan' "$YAML")"

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
