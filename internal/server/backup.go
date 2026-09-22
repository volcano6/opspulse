package server

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// BackupVersion is the schema version MarshalBackup writes and ParseBackup
// accepts.
//
// The blob is a single 1Password Secure Note field, so a change to its shape has
// to be recognised rather than guessed at: an older OpsPulse reading a newer
// blob must fail loudly instead of silently dropping the fields it does not know.
const BackupVersion = 1

// BackupFile is one machine's complete backup: the whole servers.yaml plus every
// private key that machine holds.
//
// It is deliberately one document rather than one item per server. Every
// 1Password CLI call is a full round trip through the Desktop App - measured at
// 3-9s, with no cache on the Windows build - so a large fleet spent dozens of
// calls and over two minutes backing up. The whole payload is ~14KB, and
// 1Password round-trips a single field of 2MB byte for byte, so packing it into
// one field costs nothing and brings the same backup down to two calls.
//
// Keys is a side map rather than a field on each Server so that Servers stays
// exactly []Server: MergeInventories, SameInventory and diffServers keep working
// on a backup without a single change.
type BackupFile struct {
	Version int               `yaml:"version"`
	Machine string            `yaml:"machine,omitempty"`
	Servers []Server          `yaml:"servers"`
	Keys    map[string]string `yaml:"keys,omitempty"`
}

// MarshalBackup renders a backup document, filling in the version and refusing
// to produce one that ParseBackup would reject.
//
// Validating here rather than only on read keeps "what we upload" and "what we
// can restore" the same set. A blob that fails to parse is only discovered on
// the new machine, with nothing left to fall back on, so it must not be possible
// to write one.
//
// The servers are validated on a copy: Validate normalises Port and User in
// place, and a marshal has no business rewriting the caller's inventory.
func MarshalBackup(f BackupFile) ([]byte, error) {
	if f.Version == 0 {
		f.Version = BackupVersion
	}
	servers := append([]Server(nil), f.Servers...)
	if err := validateServers(servers); err != nil {
		return nil, fmt.Errorf("refusing to write a backup that could not be read back: %w", err)
	}
	f.Servers = servers

	out, err := yaml.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("encode the backup: %w", err)
	}
	return out, nil
}

// ParseBackup reads a backup document.
//
// It is a separate parser from ParseConfig on purpose. ParseConfig decodes the
// servers.yaml shape with KnownFields(true), so it rejects the version, machine
// and keys keys a backup carries; and a backup has to be version-checked before
// its contents mean anything.
func ParseBackup(data []byte) (BackupFile, error) {
	var f BackupFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&f); err != nil {
		return BackupFile{}, fmt.Errorf("failed to parse the backup: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return BackupFile{}, fmt.Errorf("failed to parse the backup: multiple documents are not supported")
		}
		return BackupFile{}, fmt.Errorf("failed to parse the backup: %w", err)
	}

	if f.Version < 1 || f.Version > BackupVersion {
		return BackupFile{}, fmt.Errorf("the backup is version %d, which this OpsPulse does not understand (it reads up to %d); upgrade OpsPulse on this machine", f.Version, BackupVersion)
	}
	if err := validateServers(f.Servers); err != nil {
		return BackupFile{}, fmt.Errorf("the backup holds an invalid server list: %w", err)
	}
	return f, nil
}
