package main

import (
	"io"
)

// confirmGate is the shared shape of every "this step is risky, ask first"
// check in the CLI:
//
//   - --yes answers it up front (the deliberate opt-in for scripts and CI);
//   - a non-interactive shell is refused instead of prompted at: reading a pipe
//     that never closes would hang a script, and falling through to the prompt
//     would accept a risk nobody saw;
//   - anything that is not an explicit "yes" is a refusal.
//
// The caller owns the wording, because the useful message differs per step:
// refuseErr has to tell the operator how to accept it non-interactively, and
// cancelErr has to say the run was cancelled.
//
// `ops restore run` keeps its own gate: it also has to skip the prompt under
// --dry-run, and it deliberately warns before overwriting files even in a pipe
// (see checkRestoreConfirmation).
func confirmGate(in io.Reader, out io.Writer, interactive, yes bool, prompt string, refuseErr, cancelErr error) error {
	if yes {
		return nil
	}
	if !interactive {
		return refuseErr
	}
	if !promptConfirm(in, out, prompt, false) {
		return cancelErr
	}
	return nil
}
