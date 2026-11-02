package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
)

type createUserTestExecutor struct {
	execute func(context.Context, executor.Target, string, string, io.Writer) (*executor.Result, error)
	test    func(context.Context, executor.Target) (time.Duration, string, error)
}

func (e *createUserTestExecutor) Execute(ctx context.Context, target executor.Target, taskName, script string, out io.Writer) (*executor.Result, error) {
	return e.execute(ctx, target, taskName, script, out)
}

func (e *createUserTestExecutor) Test(ctx context.Context, target executor.Target) (time.Duration, string, error) {
	return e.test(ctx, target)
}

func TestValidateCreateUserName(t *testing.T) {
	for _, name := range []string{"ubuntu", "deploy", "ops_user", "u-1"} {
		if err := validateCreateUserName(name); err != nil {
			t.Errorf("validateCreateUserName(%q) = %v, want nil", name, err)
		}
	}

	for _, name := range []string{"root", "", "Ubuntu", "1ubuntu", "ubuntu.", "ubuntu;id", "with space"} {
		if err := validateCreateUserName(name); err == nil {
			t.Errorf("validateCreateUserName(%q) = nil, want an error", name)
		}
	}
}

func TestCreateUserScriptIsIdempotentAndSafe(t *testing.T) {
	publicKey := []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest opspulse:test\n")
	script := createUserScript("ubuntu", publicKey)

	for _, want := range []string{
		"USER_NAME='ubuntu'",
		"useradd --create-home",
		"usermod -aG",
		"authorized_keys",
		"mv -f \"$AUTHORIZED_KEYS_TMP\"",
		"chmod 0700",
		"chmod 0600",
		"NOPASSWD:ALL",
		"visudo -cf",
		"runuser -u",
		"passwd -l",
		"refusing to overwrite unmanaged sudoers file",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("create-user script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, string(bytes.TrimSpace(publicKey))) {
		t.Fatal("create-user script must not embed a public key verbatim")
	}
	if strings.Contains(script, ">> \"$AUTHORIZED_KEYS\"") {
		t.Fatal("create-user script must replace authorized_keys, not append other keys")
	}
	if !strings.Contains(script, "base64 -d") {
		t.Fatal("create-user script must decode the public key from base64")
	}
}

func TestSSHLockdownScriptIsValidatedAndReversibleOnFailure(t *testing.T) {
	script := sshLockdownScript()
	for _, want := range []string{
		"PermitRootLogin no",
		"PasswordAuthentication no",
		"ChallengeResponseAuthentication no",
		"AuthenticationMethods publickey",
		"sshd_config.d",
		keyOnlySSHDropInName,
		`"$SSHD_BIN" -t`,
		`"$SSHD_BIN" -T`,
		"check_effective permitrootlogin no",
		"check_effective passwordauthentication no",
		"check_effective authenticationmethods publickey",
		"OpenSSH uses the first value it reads",
		"restore_config",
		"systemctl reload ssh.service",
		"systemctl reload sshd.service",
		"refusing to overwrite unmanaged sshd drop-in",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("SSH lockdown script missing %q:\n%s", want, script)
		}
	}
	if keyOnlySSHDropInName != "00-opspulse-key-only.conf" {
		t.Fatalf("drop-in name = %q, want it to sort before cloud-init and other machine defaults", keyOnlySSHDropInName)
	}
}

func TestGeneratedCreateUserScriptsPassBashSyntaxCheck(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	for name, script := range map[string]string{
		"create-user":  createUserScript("ubuntu", []byte("ssh-ed25519 AAAATest\n")),
		"ssh-lockdown": sshLockdownScript(),
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(bash, "-n")
			cmd.Stdin = strings.NewReader(script)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bash -n failed: %v\n%s\nscript:\n%s", err, output, script)
			}
		})
	}
}

func TestResolveCreateUserKeyGeneratesManagedKeyByDefault(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is unavailable")
	}
	home := t.TempDir()
	setTestHome(t, home)

	stored, expanded, publicKey, err := resolveCreateUserKey(
		&server.Server{Name: "web"},
		createUserOptions{},
		strings.NewReader(""),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("resolveCreateUserKey: %v", err)
	}
	if stored != "~/.ssh/opspulse_web" {
		t.Errorf("stored key path = %q, want ~/.ssh/opspulse_web", stored)
	}
	if expanded != filepath.Join(home, ".ssh", "opspulse_web") {
		t.Errorf("expanded key path = %q", expanded)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(publicKey)), "ssh-ed25519 ") {
		t.Errorf("generated public key = %q, want an ssh-ed25519 key", publicKey)
	}
}

func TestResolveCreateUserKeySuggestsManagedKeyWhenExplicitPathIsMissing(t *testing.T) {
	setTestHome(t, t.TempDir())
	missing := filepath.Join(t.TempDir(), "id_ed25519")

	_, _, _, err := resolveCreateUserKey(
		&server.Server{Name: "web"},
		createUserOptions{keyPath: missing},
		strings.NewReader(""),
		io.Discard,
	)
	if err == nil {
		t.Fatal("resolveCreateUserKey = nil, want a missing key error")
	}
	for _, want := range []string{"private key file", "omit --key", "~/.ssh/opspulse_web"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
}

func createUserTestStore(t *testing.T, srv server.Server) *server.Store {
	t.Helper()
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	if err := store.Save(srv); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return store
}

func createUserTestDeps(store *server.Store, exec executor.Executor) createUserDeps {
	return createUserDeps{
		store:    store,
		executor: exec,
		resolveKey: func(*server.Server, createUserOptions, io.Reader, io.Writer) (string, string, []byte, error) {
			return "~/.ssh/opspulse_web", "/tmp/opspulse_web", []byte("ssh-ed25519 AAAATest\n"), nil
		},
		interactive: false,
	}
}

func TestRunServerCreateUserAppliesKeyOnlySSHAfterVerification(t *testing.T) {
	srv := server.Server{Name: "web", Host: "192.0.2.10", Port: 22, User: "root", Password: "old-pass"}
	store := createUserTestStore(t, srv)

	var tasks []string
	var testTargets []executor.Target
	fake := &createUserTestExecutor{
		execute: func(_ context.Context, target executor.Target, taskName, _ string, _ io.Writer) (*executor.Result, error) {
			tasks = append(tasks, taskName)
			if target.Server == nil {
				t.Fatal("execute target has no server")
			}
			return &executor.Result{Success: true}, nil
		},
		test: func(_ context.Context, target executor.Target) (time.Duration, string, error) {
			testTargets = append(testTargets, target)
			return time.Millisecond, "Linux", nil
		},
	}

	var out bytes.Buffer
	if err := runServerCreateUser(context.Background(), strings.NewReader(""), &out, "web", createUserOptions{
		username: "ubuntu",
		yes:      true,
	}, createUserTestDeps(store, fake)); err != nil {
		t.Fatalf("runServerCreateUser: %v", err)
	}

	if got, want := strings.Join(tasks, ","), "create-user,verify-user-sudo,lockdown-ssh,verify-user-sudo"; got != want {
		t.Fatalf("remote tasks = %q, want %q", got, want)
	}
	if len(testTargets) != 2 {
		t.Fatalf("connection tests = %d, want 2", len(testTargets))
	}
	for _, target := range testTargets {
		newTarget := target.Server
		if newTarget.User != "ubuntu" || newTarget.KeyPath != "~/.ssh/opspulse_web" || newTarget.Password != "" {
			t.Fatalf("verification target = %+v, want ubuntu with the new key and no fallback credential", newTarget)
		}
	}

	stored, err := store.Get("web")
	if err != nil {
		t.Fatal(err)
	}
	if stored.User != "ubuntu" || stored.KeyPath != "~/.ssh/opspulse_web" || stored.Password != "" {
		t.Fatalf("stored server = %+v, want switched to ubuntu/key with fallback credential cleared", stored)
	}
}

func TestRunServerCreateUserDoesNotChangeAnythingIfUserVerificationFails(t *testing.T) {
	srv := server.Server{Name: "web", Host: "192.0.2.10", Port: 22, User: "root", Password: "old-pass"}
	store := createUserTestStore(t, srv)

	var tasks []string
	fake := &createUserTestExecutor{
		execute: func(_ context.Context, _ executor.Target, taskName, _ string, _ io.Writer) (*executor.Result, error) {
			tasks = append(tasks, taskName)
			return &executor.Result{Success: true}, nil
		},
		test: func(_ context.Context, _ executor.Target) (time.Duration, string, error) {
			return 0, "", errors.New("key rejected")
		},
	}

	err := runServerCreateUser(context.Background(), strings.NewReader(""), io.Discard, "web", createUserOptions{
		username: "ubuntu",
		yes:      true,
	}, createUserTestDeps(store, fake))
	if err == nil || !strings.Contains(err.Error(), "servers.yaml was not changed") {
		t.Fatalf("runServerCreateUser error = %v, want a verification failure", err)
	}
	if strings.Join(tasks, ",") != "create-user" {
		t.Fatalf("remote tasks = %v, want only create-user", tasks)
	}

	stored, getErr := store.Get("web")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stored.User != "root" || stored.Password != "old-pass" {
		t.Fatalf("store changed after failed verification: %+v", stored)
	}
}

func TestRunServerCreateUserRefusesSSHLockdownWithoutConfirmation(t *testing.T) {
	srv := server.Server{Name: "web", Host: "192.0.2.10", Port: 22, User: "root"}
	store := createUserTestStore(t, srv)

	executed := false
	fake := &createUserTestExecutor{
		execute: func(context.Context, executor.Target, string, string, io.Writer) (*executor.Result, error) {
			executed = true
			return &executor.Result{Success: true}, nil
		},
		test: func(context.Context, executor.Target) (time.Duration, string, error) {
			return 0, "", nil
		},
	}

	err := runServerCreateUser(context.Background(), strings.NewReader(""), io.Discard, "web", createUserOptions{
		username: "ubuntu",
	}, createUserTestDeps(store, fake))
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("runServerCreateUser error = %v, want a --yes refusal", err)
	}
	if executed {
		t.Fatal("non-interactive command without --yes must not execute remote changes")
	}
}

func TestRunServerCreateUserCancelledByUser(t *testing.T) {
	srv := server.Server{Name: "web", Host: "192.0.2.10", Port: 22, User: "root"}
	store := createUserTestStore(t, srv)
	deps := createUserTestDeps(store, &createUserTestExecutor{})
	deps.interactive = true

	err := runServerCreateUser(context.Background(), strings.NewReader("n\n"), io.Discard, "web", createUserOptions{
		username: "ubuntu",
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("runServerCreateUser error = %v, want cancellation", err)
	}
}

func TestRunServerCreateUserPostLockdownVerificationFailureIsExplicit(t *testing.T) {
	srv := server.Server{Name: "web", Host: "192.0.2.10", Port: 22, User: "root"}
	store := createUserTestStore(t, srv)

	testCalls := 0
	var tasks []string
	fake := &createUserTestExecutor{
		execute: func(_ context.Context, _ executor.Target, taskName, _ string, _ io.Writer) (*executor.Result, error) {
			tasks = append(tasks, taskName)
			return &executor.Result{Success: true}, nil
		},
		test: func(_ context.Context, _ executor.Target) (time.Duration, string, error) {
			testCalls++
			if testCalls == 2 {
				return 0, "", errors.New("connection refused after reload")
			}
			return time.Millisecond, "Linux", nil
		},
	}

	err := runServerCreateUser(context.Background(), strings.NewReader(""), io.Discard, "web", createUserOptions{
		username: "ubuntu",
		yes:      true,
	}, createUserTestDeps(store, fake))
	if err == nil {
		t.Fatal("runServerCreateUser = nil, want post-lockdown verification failure")
	}
	for _, want := range []string{"key-only SSH and root login disablement were applied", "provider console", keyOnlySSHDropInName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want %q", err, want)
		}
	}
	if strings.Join(tasks, ",") != "create-user,verify-user-sudo,lockdown-ssh" {
		t.Fatalf("remote tasks = %v", tasks)
	}
}

func TestServerCreateUserFlagsRegistered(t *testing.T) {
	for _, name := range []string{"key", "no-copy-key", "yes"} {
		if serverCreateUserCmd.Flags().Lookup(name) == nil {
			t.Errorf("ops server create-user should expose --%s", name)
		}
	}
	if serverCreateUserCmd.Flags().Lookup("disable-root") != nil {
		t.Error("root SSH should always be disabled; --disable-root must not be exposed")
	}
}
