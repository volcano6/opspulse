package executor

import "time"

// Result holds the execution outcome of a script on a server.
type Result struct {
	ServerName string        `json:"server_name"`
	Template   string        `json:"template"`
	Success    bool          `json:"success"`
	ExitCode   int           `json:"exit_code"`
	Duration   time.Duration `json:"duration"`
	StartTime  time.Time     `json:"start_time"`
	EndTime    time.Time     `json:"end_time"`
	Error      error         `json:"error,omitempty"`
	LogPath    string        `json:"log_path,omitempty"`
}
