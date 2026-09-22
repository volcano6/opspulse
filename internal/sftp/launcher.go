package sftp

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/volcano6/opspulse/internal/server"
)

// Materialized1PKeyDir returns the directory where temporary private keys are stored
// for GUI SFTP clients (~/.ssh/opspulse-1p).
//
// Legacy: keys are no longer materialised there. The directory is only
// inspected and purged to clean up residue left by versions that resolved
// op:// references at connect time. Slated for removal in the next major
// version.
func Materialized1PKeyDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "opspulse-1p"), nil
}

// MaterializedKeyInfo describes a temporary private key file in ~/.ssh/opspulse-1p.
type MaterializedKeyInfo struct {
	Name    string
	ModTime time.Time
	IsStale bool // True if older than 24 hours
}

// ListMaterialized1PKeyDetails returns detailed metadata for all materialized keys in ~/.ssh/opspulse-1p.
//
// Legacy: see Materialized1PKeyDir.
func ListMaterialized1PKeyDetails() ([]MaterializedKeyInfo, error) {
	dir, err := Materialized1PKeyDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var details []MaterializedKeyInfo
	now := time.Now()
	for _, entry := range entries {
		if !entry.IsDir() && server.ValidateServerName(entry.Name()) == nil {
			info, err := entry.Info()
			if err != nil {
				continue
			}
			stale := now.Sub(info.ModTime()) > 24*time.Hour
			details = append(details, MaterializedKeyInfo{
				Name:    entry.Name(),
				ModTime: info.ModTime(),
				IsStale: stale,
			})
		}
	}
	return details, nil
}

// ListMaterialized1PKeys returns server names of all materialized keys in ~/.ssh/opspulse-1p.
//
// Legacy: see Materialized1PKeyDir.
func ListMaterialized1PKeys() ([]string, error) {
	details, err := ListMaterialized1PKeyDetails()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(details))
	for _, d := range details {
		keys = append(keys, d.Name)
	}
	return keys, nil
}

func materializedKeyPath(dir, serverName string) (string, error) {
	if err := server.ValidateServerName(serverName); err != nil {
		return "", fmt.Errorf("invalid server name %q: %w", serverName, err)
	}
	target := filepath.Join(dir, serverName)
	rel, err := filepath.Rel(dir, target)
	if err != nil || rel != serverName || strings.HasPrefix(rel, "..") || filepath.IsAbs(serverName) {
		return "", fmt.Errorf("path traversal detected for %q", serverName)
	}
	return target, nil
}

// PurgeMaterialized1PKeys deletes temporary private keys from ~/.ssh/opspulse-1p.
// If serverName is non-empty, only that server's key is removed.
// If serverName is empty, all keys in the directory are removed.
// Returns the list of deleted server key names and any encountered error.
//
// Legacy: it exists to clean up residue from the op:// era. `ops 1p restore`
// calls it once after migrating. `ops sftp --cleanup` is deprecated and kept
// only so that an existing script does not break outright.
func PurgeMaterialized1PKeys(serverName string) ([]string, error) {
	dir, err := Materialized1PKeyDir()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if serverName != "" {
			if valErr := server.ValidateServerName(serverName); valErr != nil {
				return nil, fmt.Errorf("invalid server name %q: %w", serverName, valErr)
			}
		}
		return nil, nil
	}

	var deleted []string
	if serverName != "" {
		target, err := materializedKeyPath(dir, serverName)
		if err != nil {
			return nil, err
		}
		if fi, err := os.Lstat(target); err == nil {
			if fi.IsDir() {
				return nil, fmt.Errorf("refusing to remove directory %s as server key", target)
			}
			if removeErr := os.Remove(target); removeErr != nil {
				return nil, fmt.Errorf("remove key file for %s: %w", serverName, removeErr)
			}
			deleted = append(deleted, serverName)
		}
	} else {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return nil, readErr
		}
		for _, entry := range entries {
			target := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if removeErr := os.RemoveAll(target); removeErr != nil {
					return deleted, fmt.Errorf("remove directory %s: %w", target, removeErr)
				}
			} else {
				if removeErr := os.Remove(target); removeErr != nil {
					return deleted, fmt.Errorf("remove key file %s: %w", target, removeErr)
				}
				deleted = append(deleted, entry.Name())
			}
		}
	}

	// Clean up parent directory if empty
	if remaining, err := os.ReadDir(dir); err == nil && len(remaining) == 0 {
		_ = os.Remove(dir)
	}

	return deleted, nil
}

// ClientType denotes the category of SFTP client.
type ClientType string

const (
	// ClientWinSCP represents the WinSCP client on Windows.
	ClientWinSCP ClientType = "winscp"
	// ClientXftp represents NetSarang Xftp on Windows.
	ClientXftp ClientType = "xftp"
	// ClientFileZilla represents the FileZilla FTP/SFTP client.
	ClientFileZilla ClientType = "filezilla"
	// ClientCyberduck represents Cyberduck on macOS.
	ClientCyberduck ClientType = "cyberduck"
	// ClientTransmit represents Panic Transmit on macOS.
	ClientTransmit ClientType = "transmit"
	// ClientSystem represents the operating system's default protocol handler.
	ClientSystem ClientType = "system"
	// ClientOpenSSH represents the standard terminal OpenSSH sftp command.
	ClientOpenSSH ClientType = "openssh"
)

// ClientInfo describes a detected or configured SFTP client.
type ClientInfo struct {
	Type        ClientType
	Name        string
	Path        string
	IsGUI       bool
	Description string
}

// DetectAvailableClients discovers all installed GUI and CLI SFTP clients on the host system.
func DetectAvailableClients() []ClientInfo {
	var clients []ClientInfo

	switch runtime.GOOS {
	case "windows":
		clients = append(clients, detectWindowsClients()...)
	case "darwin":
		clients = append(clients, detectDarwinClients()...)
	default:
		if IsWSL() {
			clients = append(clients, detectWindowsClients()...)
		}
		clients = append(clients, detectLinuxClients()...)
	}

	// Always detect OpenSSH sftp as terminal fallback
	if sftpPath, err := exec.LookPath("sftp"); err == nil {
		clients = append(clients, ClientInfo{
			Type:        ClientOpenSSH,
			Name:        "OpenSSH sftp",
			Path:        sftpPath,
			IsGUI:       false,
			Description: "Standard interactive terminal SFTP client",
		})
	}

	return clients
}

func resolveWinPath(p string) string {
	if IsWSL() {
		return translateWindowsPathToWSL(p)
	}
	return p
}

func detectWindowsClients() []ClientInfo {
	var results []ClientInfo

	// 1. WinSCP
	winSCPLocations := []string{
		resolveWinPath(`C:\Program Files (x86)\WinSCP\WinSCP.exe`),
		resolveWinPath(`C:\Program Files\WinSCP\WinSCP.exe`),
	}
	// In WSL, os.Getenv("LOCALAPPDATA") is empty, but we can't easily get it.
	// We rely on standard paths or PATH.
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		winSCPLocations = append(winSCPLocations, filepath.Join(localAppData, "Programs", "WinSCP", "WinSCP.exe"))
	}
	if p, err := exec.LookPath("WinSCP.exe"); err == nil {
		winSCPLocations = append([]string{p}, winSCPLocations...)
	}
	if p := findFirstExistingFile(winSCPLocations); p != "" {
		results = append(results, ClientInfo{
			Type:        ClientWinSCP,
			Name:        "WinSCP",
			Path:        p,
			IsGUI:       true,
			Description: "Windows graphical SFTP/FTP/SCP client",
		})
	}

	// 2. Xftp (NetSarang)
	xftpLocations := []string{
		resolveWinPath(`C:\Program Files (x86)\NetSarang\Xftp 8\Xftp.exe`),
		resolveWinPath(`C:\Program Files\NetSarang\Xftp 8\Xftp.exe`),
		resolveWinPath(`C:\Program Files (x86)\NetSarang\Xftp 7\Xftp.exe`),
		resolveWinPath(`C:\Program Files\NetSarang\Xftp 7\Xftp.exe`),
	}
	if p, err := exec.LookPath("Xftp.exe"); err == nil {
		xftpLocations = append([]string{p}, xftpLocations...)
	}
	if p := findFirstExistingFile(xftpLocations); p != "" {
		results = append(results, ClientInfo{
			Type:        ClientXftp,
			Name:        "Xftp",
			Path:        p,
			IsGUI:       true,
			Description: "NetSarang Xftp secure file transfer client",
		})
	}

	// 3. FileZilla
	fzLocations := []string{
		resolveWinPath(`C:\Program Files\FileZilla FTP Client\filezilla.exe`),
		resolveWinPath(`C:\Program Files (x86)\FileZilla FTP Client\filezilla.exe`),
	}
	if p, err := exec.LookPath("filezilla.exe"); err == nil {
		fzLocations = append([]string{p}, fzLocations...)
	}
	if p := findFirstExistingFile(fzLocations); p != "" {
		results = append(results, ClientInfo{
			Type:        ClientFileZilla,
			Name:        "FileZilla",
			Path:        p,
			IsGUI:       true,
			Description: "Cross-platform graphical FTP/SFTP client",
		})
	}

	return results
}

func detectDarwinClients() []ClientInfo {
	var results []ClientInfo

	if pathExists("/Applications/Cyberduck.app") {
		results = append(results, ClientInfo{
			Type:        ClientCyberduck,
			Name:        "Cyberduck",
			Path:        "/Applications/Cyberduck.app",
			IsGUI:       true,
			Description: "macOS cloud storage and SFTP browser",
		})
	}
	if pathExists("/Applications/FileZilla.app") {
		results = append(results, ClientInfo{
			Type:        ClientFileZilla,
			Name:        "FileZilla",
			Path:        "/Applications/FileZilla.app",
			IsGUI:       true,
			Description: "Cross-platform graphical FTP/SFTP client",
		})
	}
	if pathExists("/Applications/Transmit.app") {
		results = append(results, ClientInfo{
			Type:        ClientTransmit,
			Name:        "Transmit",
			Path:        "/Applications/Transmit.app",
			IsGUI:       true,
			Description: "macOS file transfer client by Panic",
		})
	}

	return results
}

func detectLinuxClients() []ClientInfo {
	var results []ClientInfo

	if p, err := exec.LookPath("filezilla"); err == nil {
		results = append(results, ClientInfo{
			Type:        ClientFileZilla,
			Name:        "FileZilla",
			Path:        p,
			IsGUI:       true,
			Description: "Cross-platform graphical FTP/SFTP client",
		})
	}
	if p, err := exec.LookPath("nautilus"); err == nil {
		results = append(results, ClientInfo{
			Type:        ClientSystem,
			Name:        "Nautilus (Files)",
			Path:        p,
			IsGUI:       true,
			Description: "GNOME Files SFTP location handler",
		})
	}
	if p, err := exec.LookPath("xdg-open"); err == nil {
		results = append(results, ClientInfo{
			Type:        ClientSystem,
			Name:        "xdg-open",
			Path:        p,
			IsGUI:       true,
			Description: "Default desktop environment URL handler",
		})
	}

	return results
}

func findFirstExistingFile(candidates []string) string {
	for _, c := range candidates {
		if c != "" && pathExists(c) {
			return c
		}
	}
	return ""
}

func pathExists(p string) bool {
	// #nosec G703
	info, err := os.Stat(filepath.Clean(p))
	return err == nil && !info.IsDir()
}

// FindClient resolves a preferred client name or discovers the best available client.
func FindClient(preference string, preferCLI bool) (*ClientInfo, error) {
	available := DetectAvailableClients()

	if preferCLI {
		for _, c := range available {
			if c.Type == ClientOpenSSH {
				return &c, nil
			}
		}
		if p, err := exec.LookPath("sftp"); err == nil {
			return &ClientInfo{
				Type:        ClientOpenSSH,
				Name:        "OpenSSH sftp",
				Path:        p,
				IsGUI:       false,
				Description: "Standard interactive terminal SFTP client",
			}, nil
		}
		return nil, errors.New("OpenSSH 'sftp' binary was not found in system PATH")
	}

	// If explicit preference is provided
	if preference != "" {
		cleanPref := strings.ToLower(strings.TrimSpace(preference))

		// 1. Direct executable file path
		if pathExists(preference) {
			return &ClientInfo{
				Type:        guessClientTypeFromPath(preference),
				Name:        filepath.Base(preference),
				Path:        preference,
				IsGUI:       true,
				Description: "User-specified custom SFTP application",
			}, nil
		}

		// 2. Match against detected clients
		for _, c := range available {
			if strings.ToLower(string(c.Type)) == cleanPref ||
				strings.Contains(strings.ToLower(c.Name), cleanPref) ||
				strings.Contains(strings.ToLower(filepath.Base(c.Path)), cleanPref) {
				return &c, nil
			}
		}

		// 3. Try to look up in system PATH by preference name
		if p, err := exec.LookPath(preference); err == nil {
			return &ClientInfo{
				Type:        guessClientTypeFromPath(p),
				Name:        filepath.Base(p),
				Path:        p,
				IsGUI:       true,
				Description: "Custom application from PATH",
			}, nil
		}

		return nil, fmt.Errorf("requested SFTP client %q was not found on this system", preference)
	}

	// Default: pick the first available GUI client
	for _, c := range available {
		if c.IsGUI {
			return &c, nil
		}
	}

	// Fallback to CLI OpenSSH if no GUI client is present
	for _, c := range available {
		if c.Type == ClientOpenSSH {
			return &c, nil
		}
	}

	return nil, errors.New("no supported GUI SFTP client (WinSCP, Xftp, FileZilla, Cyberduck) or OpenSSH sftp found")
}

func guessClientTypeFromPath(p string) ClientType {
	base := strings.ToLower(filepath.Base(p))
	switch {
	case strings.Contains(base, "winscp"):
		return ClientWinSCP
	case strings.Contains(base, "xftp"):
		return ClientXftp
	case strings.Contains(base, "filezilla"):
		return ClientFileZilla
	case strings.Contains(base, "cyberduck"):
		return ClientCyberduck
	case strings.Contains(base, "transmit"):
		return ClientTransmit
	default:
		return ClientSystem
	}
}

// BuildLaunchCommand prepares the *exec.Cmd to launch the specified SFTP client.
func BuildLaunchCommand(client ClientInfo, srv server.Server, remotePath string) (*exec.Cmd, error) {
	if remotePath == "" {
		remotePath = "/"
	}
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}

	port := srv.Port
	if port <= 0 {
		port = 22
	}
	user := srv.User
	if user == "" {
		user = "root"
	}

	// This builds a command line for an external sftp client and never reaches
	// executor.BuildClientConfig, so the op:// guard has to be applied here.
	// Without it the literal "op://..." string would be handed to the client as
	// a key path.
	if err := srv.RejectLegacy1PRefs(); err != nil {
		return nil, err
	}

	keyPathForClient := srv.KeyPath
	if keyPathForClient != "" {
		if strings.HasPrefix(keyPathForClient, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				if keyPathForClient == "~" {
					keyPathForClient = home
				} else if strings.HasPrefix(keyPathForClient, "~/") || strings.HasPrefix(keyPathForClient, "~\\") {
					keyPathForClient = filepath.Join(home, keyPathForClient[2:])
				}
			}
		}
	}
	if IsWSL() && client.IsGUI && keyPathForClient != "" {
		bridged, err := BridgeKeyToWindows(keyPathForClient)
		if err != nil {
			return nil, fmt.Errorf("WSL key bridge failed: %w", err)
		}
		keyPathForClient = bridged
	}

	switch client.Type {
	case ClientWinSCP:
		// WinSCP supports: WinSCP.exe "sftp://user@host:port/path" [/privatekey="..."]
		targetURL := formatSFTPURL(user, srv.Password, srv.Host, port, remotePath, srv.KeyPath == "")
		args := []string{targetURL}
		if keyPathForClient != "" {
			args = append(args, fmt.Sprintf("/privatekey=%s", filepath.Clean(keyPathForClient)))
		}
		return exec.Command(client.Path, args...), nil

	case ClientXftp:
		// Xftp supports: Xftp.exe sftp://user[:password]@host:port/path
		targetURL := formatSFTPURL(user, srv.Password, srv.Host, port, remotePath, true)
		return exec.Command(client.Path, targetURL), nil

	case ClientFileZilla:
		// FileZilla supports: filezilla "sftp://user[:password]@host:port/path"
		targetURL := formatSFTPURL(user, srv.Password, srv.Host, port, remotePath, true)
		if runtime.GOOS == "darwin" && strings.HasSuffix(client.Path, ".app") {
			return exec.Command("open", "-a", client.Path, targetURL), nil
		}
		return exec.Command(client.Path, targetURL), nil

	case ClientCyberduck, ClientTransmit:
		// macOS open -a <App> "sftp://..."
		targetURL := formatSFTPURL(user, srv.Password, srv.Host, port, remotePath, false)
		return exec.Command("open", "-a", client.Name, targetURL), nil

	case ClientSystem:
		targetURL := formatSFTPURL(user, srv.Password, srv.Host, port, remotePath, false)
		if runtime.GOOS == "windows" {
			return exec.Command("cmd", "/c", "start", "", targetURL), nil
		}
		if runtime.GOOS == "darwin" {
			return exec.Command("open", targetURL), nil
		}
		return exec.Command(client.Path, targetURL), nil

	case ClientOpenSSH:
		// Terminal OpenSSH sftp command
		args := []string{"-P", strconv.Itoa(port)}
		if keyPathForClient != "" {
			args = append(args, "-i", filepath.Clean(keyPathForClient))
		}
		target := fmt.Sprintf("%s@%s", user, srv.Host)
		if remotePath != "/" {
			target = fmt.Sprintf("%s:%s", target, remotePath)
		}
		args = append(args, target)
		return exec.Command(client.Path, args...), nil

	default:
		targetURL := formatSFTPURL(user, srv.Password, srv.Host, port, remotePath, true)
		return exec.Command(client.Path, targetURL), nil
	}
}

func formatSFTPURL(user, password, host string, port int, path string, includePassword bool) string {
	hostPort := net.JoinHostPort(host, strconv.Itoa(port))
	u := &url.URL{
		Scheme: "sftp",
		Host:   hostPort,
		Path:   path,
	}

	if includePassword && password != "" {
		u.User = url.UserPassword(user, password)
	} else if user != "" {
		u.User = url.User(user)
	}

	return u.String()
}

// LaunchAsync executes a GUI client command in a detached, non-blocking process.
func LaunchAsync(cmd *exec.Cmd) error {
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Start()
}
