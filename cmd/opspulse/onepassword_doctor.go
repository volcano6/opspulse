package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/platform"
	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
)

var (
	onePasswordDoctorVault   string
	onePasswordDoctorOffline bool
)

// doctorStep is one line of 'ops 1p doctor' output. status is one of
// doctorOK / doctorWarn / doctorFail / doctorSkip; renderDoctorSteps turns the
// whole slice into a human report and reports whether anything failed.
type doctorStep struct {
	status string
	label  string
	detail string
	hint   string
}

const (
	doctorOK   = "ok"
	doctorWarn = "warn"
	doctorFail = "fail"
	doctorSkip = "skip"
)

var onePasswordDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check this machine's 1Password integration end to end, changing nothing",
	Long: `Walk the whole 1Password path once, in read-only mode, and say where it breaks.

The command checks, in order: whether the 'op' executable is present and which
build it is (WSL must use the Windows build), whether an account is visible,
whether the vault can be listed and which vault would be used, whether this
machine's backup item exists and what it holds, and finally whether servers.yaml
still points at credentials that have not been restored locally.

Nothing is written: no vault, no account and no servers.yaml entry is changed.
Every step is reported as ok / warn / fail, and at least one fail makes the
command exit non-zero, so it can be used as a pre-flight check.

Use --offline to run only the checks that need no 1Password round trip.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runOnePasswordDoctor(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	onePasswordDoctorCmd.Flags().StringVar(&onePasswordDoctorVault, "vault", "", "Vault to inspect (default: remembered setting, then $OP_VAULT, then the only accessible vault)")
	onePasswordDoctorCmd.Flags().BoolVar(&onePasswordDoctorOffline, "offline", false, "Only run the checks that need no 1Password round trip")
}

// runOnePasswordDoctor performs the checks and renders them. It returns a
// non-nil error when any step failed, so the exit status carries the verdict.
func runOnePasswordDoctor(ctx context.Context, out io.Writer) error {
	var steps []doctorStep
	add := func(status, label, detail, hint string) {
		steps = append(steps, doctorStep{status: status, label: label, detail: detail, hint: hint})
	}

	if platform.IsWSL() {
		add(doctorOK, "host", "WSL detected: the integration must use the Windows build of 'op'", "")
	} else {
		add(doctorOK, "host", "native host", "")
	}

	cli := secret.Detect()
	if !cli.Available() {
		add(doctorFail, "op executable", "not found on this host", secret.InstallHint())
		if platform.IsWSL() {
			if hint := platform.WSLFMaskHint(); hint != "" {
				steps[len(steps)-1].hint = steps[len(steps)-1].hint + "\n" + hint
			}
		}
		return finishDoctor(out, steps)
	}
	build := "Linux/Unix build"
	if cli.IsWindowsBinary {
		build = "Windows build"
	}
	add(doctorOK, "op executable", fmt.Sprintf("%s (%s)", cli.Path, build), "")

	settings, err := secret.LoadSettings()
	if err != nil {
		add(doctorWarn, "remembered settings", err.Error(), "run 'ops 1p config --unset' to clear the settings file")
	} else if settings.IsZero() {
		add(doctorOK, "remembered settings", "nothing remembered (vault and account are inferred per run)", "")
	} else {
		add(doctorOK, "remembered settings", describeRemembered(settings), "")
	}

	servers, storeErr := localServersForDoctor()
	if storeErr != nil {
		add(doctorWarn, "servers.yaml", storeErr.Error(), "")
	} else {
		add(doctorOK, "servers.yaml", fmt.Sprintf("%d server(s)", len(servers)), "")
	}

	if onePasswordDoctorOffline {
		add(doctorSkip, "1Password round trips", "skipped by --offline", "re-run without --offline to check the account, vault and backup item")
		return finishDoctor(out, steps)
	}

	cli = applyOnePasswordAccount(cli)

	accounts, err := listOnePasswordAccounts(ctx, cli)
	switch {
	case err != nil && secret.AuthFailure(err):
		add(doctorFail, "account", "1Password CLI is installed but not authorised", onePasswordAuthHint(cli))
	case err != nil:
		add(doctorFail, "account", err.Error(), onePasswordStalledHint)
	case len(accounts) == 0:
		add(doctorWarn, "account", "no account is configured for the CLI", "sign in with 'op account add' or open the 1Password desktop app")
	default:
		add(doctorOK, "account", describeAccounts(accounts), "")
	}

	// The vault resolves against the same precedence every other command uses,
	// but nothing is remembered here: doctor must not change state.
	vault, err := resolveAndValidateVault(ctx, cli, onePasswordDoctorVault, false)
	if err != nil {
		add(doctorFail, "vault", err.Error(), "")
		return finishDoctor(out, steps)
	}
	add(doctorOK, "vault", fmt.Sprintf("%q", vault), describeVaultSource(onePasswordDoctorVault, settings))

	title := backupTitle()
	ref := secret.BuildInventoryRef(vault, title)
	payload, err := cli.Run(ctx, "read", ref, "--no-newline")
	switch {
	case err != nil && strings.Contains(err.Error(), itemMissingMarker):
		add(doctorWarn, "backup item", fmt.Sprintf("%q does not exist yet", title), "run 'ops 1p backup' to create it")
	case err != nil && secret.AuthFailure(err):
		add(doctorFail, "backup item", err.Error(), onePasswordAuthHint(cli))
	case err != nil:
		add(doctorFail, "backup item", err.Error(), onePasswordFailureHint(cli, err))
	default:
		detail, parseErr := describeBackupPayload(payload)
		if parseErr != nil {
			add(doctorFail, "backup item", parseErr.Error(), "the item exists but is not a readable backup document; 'ops 1p backup' will refuse to overwrite it blindly")
		} else {
			add(doctorOK, "backup item", detail, "")
		}
	}

	refs, missingKeys := localCredentialGaps(servers)
	if len(refs) > 0 {
		add(doctorWarn, "servers.yaml credentials", fmt.Sprintf("%d server(s) still hold an op:// reference: %s", len(refs), strings.Join(refs, ", ")),
			"run 'ops 1p restore' to resolve them into local credentials")
	}
	if len(missingKeys) > 0 {
		add(doctorWarn, "private key files", fmt.Sprintf("%d server(s) point at a key file that is not on this machine: %s", len(missingKeys), strings.Join(missingKeys, ", ")),
			"run 'ops 1p restore <name>' to write the backed-up key to disk")
	}
	if len(refs) == 0 && len(missingKeys) == 0 {
		add(doctorOK, "local credentials", "every server resolves without a 1Password round trip", "")
	}

	return finishDoctor(out, steps)
}

// finishDoctor renders the report and turns any failure into a non-nil error.
func finishDoctor(out io.Writer, steps []doctorStep) error {
	failed := renderDoctorSteps(out, steps)
	if failed {
		return errors.New("1Password integration check failed; see the ❌ lines above")
	}
	return nil
}

// renderDoctorSteps writes one line per step and returns whether any of them
// failed. Split out (and exported to tests through the package) so the verdict
// rule is testable without touching a real terminal.
func renderDoctorSteps(out io.Writer, steps []doctorStep) bool {
	failed, warned := false, false
	for _, s := range steps {
		marker := "✅"
		switch s.status {
		case doctorWarn:
			marker, warned = "⚠️ ", true
		case doctorFail:
			marker, failed = "❌", true
		case doctorSkip:
			marker = "⏭️ "
		}

		line := fmt.Sprintf("%s %s: %s", marker, s.label, s.detail)
		_, _ = fmt.Fprintln(out, line)
		if s.hint != "" {
			for _, hint := range strings.Split(s.hint, "\n") {
				if strings.TrimSpace(hint) == "" {
					continue
				}
				_, _ = fmt.Fprintf(out, "   💡 %s\n", hint)
			}
		}
	}

	switch {
	case failed:
		_, _ = fmt.Fprintln(out, "\nSome checks failed: fix the ❌ lines above and run 'ops 1p doctor' again.")
	case warned:
		_, _ = fmt.Fprintln(out, "\nThe integration works, with the caveats listed above.")
	default:
		_, _ = fmt.Fprintln(out, "\nThe 1Password integration is healthy on this machine.")
	}
	return failed
}

// localServersForDoctor reads the inventory without creating it: doctor is a
// read-only command and must not leave an empty servers.yaml behind.
func localServersForDoctor() ([]server.Server, error) {
	store := server.NewDefaultStore()
	return store.List()
}

// localCredentialGaps reports the two ways servers.yaml can reference something
// only 1Password holds: an unresolved op:// reference, and a key path whose file
// is absent here. Both are expected right after a restore onto a fresh machine,
// and both are what 'ops 1p restore' exists to fix.
func localCredentialGaps(servers []server.Server) (refs []string, missingKeys []string) {
	for _, srv := range servers {
		if secret.Is1PRef(srv.KeyPath) || secret.Is1PRef(srv.Password) {
			refs = append(refs, srv.Name)
		}
	}
	return refs, serversWithMissingKeyFiles(servers)
}

// describeBackupPayload summarises the machine's backup document without ever
// echoing a credential: counts only. The item is a Secure Note holding the whole
// servers.yaml and every private key, so a size in bytes plus the server/key
// counts is what tells the user whether it is the document they expect.
func describeBackupPayload(payload []byte) (string, error) {
	file, err := server.ParseBackup(payload)
	if err != nil {
		return "", fmt.Errorf("read the backup document back: %w", err)
	}
	plaintext, withKey := 0, 0
	for _, srv := range file.Servers {
		if strings.TrimSpace(srv.Password) != "" && !secret.Is1PRef(srv.Password) {
			plaintext++
		}
		if strings.TrimSpace(srv.KeyPath) != "" {
			withKey++
		}
	}
	return fmt.Sprintf("%d byte(s): %d server(s), %d private key(s), %d plaintext password(s), %d server(s) naming a key",
		len(payload), len(file.Servers), len(file.Keys), plaintext, withKey), nil
}

func describeRemembered(settings secret.Settings) string {
	parts := make([]string, 0, 2)
	if v := strings.TrimSpace(settings.Vault); v != "" {
		parts = append(parts, fmt.Sprintf("vault %q", v))
	}
	if a := strings.TrimSpace(settings.Account); a != "" {
		parts = append(parts, fmt.Sprintf("account %q", a))
	}
	return strings.Join(parts, ", ")
}

func describeAccounts(accounts []onePasswordAccountInfo) string {
	labels := make([]string, 0, len(accounts))
	for _, acct := range accounts {
		if acct.Email != "" && acct.URL != "" {
			labels = append(labels, fmt.Sprintf("%s (%s)", acct.Email, acct.URL))
			continue
		}
		if acct.URL != "" {
			labels = append(labels, acct.URL)
			continue
		}
		labels = append(labels, "unnamed account")
	}
	return fmt.Sprintf("%d account(s): %s", len(accounts), strings.Join(labels, ", "))
}

// describeVaultSource says why this vault was chosen, since "which vault did
// that come from" is the usual question when a backup lands somewhere
// unexpected.
func describeVaultSource(explicit string, settings secret.Settings) string {
	switch {
	case strings.TrimSpace(explicit) != "":
		return "from --vault"
	case strings.TrimSpace(settings.Vault) != "":
		return "from the remembered setting ('ops 1p config --vault')"
	default:
		return "inferred: no vault was named, so the only accessible one is used"
	}
}
