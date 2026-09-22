package secret

import "fmt"

// RejectLegacy1PRef reports an error when val is a 1Password op:// reference.
//
// OpsPulse used to resolve op:// references at connect time, which meant every
// ssh/exec/cp invocation could block on the 1Password desktop app asking for a
// password. Credentials are now local: op:// survives only as the on-disk
// format of a backup, and `ops 1p restore` migrates it. Anything still holding
// a reference is therefore a stale servers.yaml, and the only useful thing to
// do is fail with the command that fixes it.
//
// field names the offending field ("key_path" or "password") and serverName
// identifies the server, so the message points at the exact line to fix.
func RejectLegacy1PRef(field, val, serverName string) error {
	if !Is1PRef(val) {
		return nil
	}
	return fmt.Errorf("server %q uses an 'op://' %s which is no longer supported at runtime; run 'ops 1p restore' to migrate to a local credential", serverName, field)
}
