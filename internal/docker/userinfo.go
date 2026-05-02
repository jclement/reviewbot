package docker

import (
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

// getHostUserInfo returns UID, GID, and username in a cross-platform way.
// On Linux, returns actual UID/GID. On macOS/Windows (Docker Desktop),
// uses 1000/1000 as Docker Desktop handles the mapping.
func getHostUserInfo() (uid, gid, username string) {
	u, err := user.Current()
	if err != nil {
		return "1000", "1000", "dev"
	}

	username = u.Username
	// On Windows, username may include domain (DOMAIN\user)
	if idx := strings.LastIndex(username, "\\"); idx >= 0 {
		username = username[idx+1:]
	}

	// Sanitize username to be safe for Linux user creation
	username = sanitizeUsername(username)
	if username == "" {
		username = "dev"
	}

	switch runtime.GOOS {
	case "linux":
		// Use real UID/GID for native Docker
		uid = u.Uid
		gid = u.Gid
		// On some systems, Gid may be empty
		if gid == "" {
			gid = getGIDFromCommand()
		}
	default:
		// macOS and Windows use Docker Desktop which maps UID/GID
		// Use 1000 as the standard non-root UID
		uid = "1000"
		gid = "1000"
	}

	if uid == "" {
		uid = "1000"
	}
	if gid == "" {
		gid = "1000"
	}

	return uid, gid, username
}

func sanitizeUsername(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func getGIDFromCommand() string {
	if runtime.GOOS == "windows" {
		return "1000"
	}
	cmd := exec.Command("id", "-g")
	out, err := cmd.Output()
	if err != nil {
		return "1000"
	}
	return strings.TrimSpace(string(out))
}

// GetUsername returns the current username for display purposes.
func GetUsername() string {
	_, _, username := getHostUserInfo()
	return username
}

// HomeDir returns the user's home directory.
func HomeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}
