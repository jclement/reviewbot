// Package docker manages the reviewbot Docker image and review containers.
// All Docker operations go through exec.Command("docker", ...) so this works
// on any platform that has Docker installed (Linux, macOS, Windows).
package docker

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/jclement/reviewbot/internal/config"
)

// DefaultArch returns the native architecture in Docker platform format.
func DefaultArch() string { return runtime.GOARCH }

const (
	// ImageLabelVersion tracks the content hash that built the image.
	ImageLabelVersion = "dev.reviewbot.version"
	// ContainerLabel marks containers reviewbot started.
	ContainerLabel = "dev.reviewbot.run"
	// ProjectLabel stores the host project directory.
	ProjectLabel = "dev.reviewbot.project"
)

//go:embed Dockerfile
var dockerfile string

//go:embed entrypoint.sh
var entrypoint string

//go:embed orchestrator.sh
var orchestrator string

//go:embed run_agent.sh
var runAgent string

//go:embed parse_agent_output.py
var parseAgentOutput string

//go:embed render.sh
var render string

//go:embed template.html
var template string

//go:embed agents/_shared.md
var agentShared string

//go:embed all:agents
var agentsFS embed.FS

// Status describes the current state of the Docker image.
type Status int

const (
	StatusReady Status = iota
	StatusMissing
	StatusOutdated
)

// IsDockerAvailable returns true if docker is in PATH and responsive.
func IsDockerAvailable() bool {
	cmd := exec.Command("docker", "info")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// ContentHash returns a SHA-256 of the embedded image-build inputs.
// Any change to Dockerfile, entrypoint, orchestrator, render, template, or
// agents triggers a rebuild on the next run.
func ContentHash() string {
	h := sha256.New()
	h.Write([]byte(dockerfile))
	h.Write([]byte(entrypoint))
	h.Write([]byte(orchestrator))
	h.Write([]byte(runAgent))
	h.Write([]byte(parseAgentOutput))
	h.Write([]byte(render))
	h.Write([]byte(template))
	h.Write([]byte(agentShared))
	// Walk every embedded agent file deterministically.
	entries, _ := agentsFS.ReadDir("agents")
	for _, e := range entries {
		data, err := agentsFS.ReadFile("agents/" + e.Name())
		if err != nil {
			continue
		}
		h.Write([]byte(e.Name()))
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Inspect checks the state of the reviewbot image.
func Inspect(cfg *config.Config) (Status, string, error) {
	if !IsDockerAvailable() {
		return StatusMissing, "Docker is not available", fmt.Errorf("docker not found in PATH")
	}
	imageName := cfg.Image + ":" + cfg.Tag
	cmd := exec.Command("docker", "image", "inspect", imageName, "--format",
		"{{index .Config.Labels \""+ImageLabelVersion+"\"}}")
	out, err := cmd.Output()
	if err != nil {
		return StatusMissing, "Image not found", nil
	}
	label := strings.TrimSpace(string(out))
	if label == "" || label == "<no value>" {
		return StatusOutdated, "Image has no version label", nil
	}
	if label != ContentHash() {
		return StatusOutdated, fmt.Sprintf("Image %s != current %s", label[:8], ContentHash()[:8]), nil
	}
	return StatusReady, "Image is up to date", nil
}

// Build builds the reviewbot Docker image with progress callbacks.
func Build(cfg *config.Config, arch string, onProgress func(line string)) error {
	hash := ContentHash()
	imageName := cfg.Image + ":" + cfg.Tag

	buildDir, err := os.MkdirTemp("", "reviewbot-build-*")
	if err != nil {
		return fmt.Errorf("creating build dir: %w", err)
	}
	defer os.RemoveAll(buildDir)

	// Stage every embedded asset under the build context. The Dockerfile
	// COPYs them into /review/ inside the image.
	if err := os.WriteFile(buildDir+"/entrypoint.sh", []byte(entrypoint), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(buildDir+"/orchestrator.sh", []byte(orchestrator), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(buildDir+"/run_agent.sh", []byte(runAgent), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(buildDir+"/parse_agent_output.py", []byte(parseAgentOutput), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(buildDir+"/render.sh", []byte(render), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(buildDir+"/template.html", []byte(template), 0644); err != nil {
		return err
	}
	if err := os.MkdirAll(buildDir+"/agents", 0755); err != nil {
		return err
	}
	if err := os.WriteFile(buildDir+"/agents/_shared.md", []byte(agentShared), 0644); err != nil {
		return err
	}
	entries, _ := agentsFS.ReadDir("agents")
	for _, e := range entries {
		if e.Name() == "_shared.md" {
			continue
		}
		data, err := agentsFS.ReadFile("agents/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(buildDir+"/agents/"+e.Name(), data, 0644); err != nil {
			return err
		}
	}

	// Append the COPY/ENTRYPOINT directives. Keeps the embedded Dockerfile
	// concerned with packages only.
	fullDockerfile := dockerfile + `
# ── reviewbot payload ──
COPY entrypoint.sh           /entrypoint.sh
COPY orchestrator.sh         /review/orchestrator.sh
COPY run_agent.sh            /review/run_agent.sh
COPY parse_agent_output.py   /review/parse_agent_output.py
COPY render.sh               /review/render.sh
COPY template.html           /review/template.html
COPY agents/                 /review/agents/
RUN chmod +x /entrypoint.sh /review/orchestrator.sh /review/run_agent.sh /review/render.sh /review/parse_agent_output.py
ENTRYPOINT ["/entrypoint.sh"]
`
	if err := os.WriteFile(buildDir+"/Dockerfile", []byte(fullDockerfile), 0644); err != nil {
		return err
	}

	platform := "linux/" + arch
	cmd := exec.Command("docker", "build",
		"--platform", platform,
		"-t", imageName,
		"--label", fmt.Sprintf("%s=%s", ImageLabelVersion, hash),
		buildDir,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting docker build: %w", err)
	}
	buf := make([]byte, 4096)
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 && onProgress != nil {
			for _, line := range strings.Split(string(buf[:n]), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					onProgress(line)
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("docker build failed: %w\n%s", err, stderr.String())
	}
	return nil
}

// Remove removes the reviewbot image.
func Remove(cfg *config.Config) error {
	return exec.Command("docker", "rmi", cfg.Image+":"+cfg.Tag).Run()
}

// VolumeExists checks if the home volume exists.
func VolumeExists(cfg *config.Config) bool {
	cmd := exec.Command("docker", "volume", "inspect", cfg.HomeVolume)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// CreateVolume creates the home volume.
func CreateVolume(cfg *config.Config) error {
	return exec.Command("docker", "volume", "create", cfg.HomeVolume).Run()
}

// RemoveVolume removes the home volume.
func RemoveVolume(cfg *config.Config) error {
	return exec.Command("docker", "volume", "rm", cfg.HomeVolume).Run()
}

// RunOptions holds inputs for starting a review container.
type RunOptions struct {
	Name        string
	ProjectDir  string
	OutputDir   string // host path mounted to /review/out
	Config      *config.Config
	Arch        string
	BaseBranch  string // optional override
	Personality string // "" | sexy | angry | sarcastic | butler
	Staged      bool   // review staged-but-uncommitted changes
	SinceN      int    // > 0 → review last N commits
}

// RunDetached starts the review container in detached mode and returns its
// name. The orchestrator runs inside; the host's HTTP server reads OutputDir
// for live updates.
func RunDetached(opts RunOptions) error {
	imageName := opts.Config.Image + ":" + opts.Config.Tag
	uid, gid, username := getHostUserInfo()
	homeDir := "/home/" + username
	platform := "linux/" + opts.Arch

	args := []string{
		"run", "-d",
		"--platform", platform,
		"--name", opts.Name,
		"-v", opts.ProjectDir + ":/workspace:ro",
		"-v", opts.OutputDir + ":/review/out",
		"-v", opts.Config.HomeVolume + ":" + homeDir,
		"-e", "DEV_UID=" + uid,
		"-e", "DEV_GID=" + gid,
		"-e", "DEV_USER=" + username,
		"-e", "TERM=" + getEnvDefault("TERM", "xterm-256color"),
		"-e", "LANG=en_US.UTF-8",
		"-e", "REVIEWBOT_PARALLEL=" + fmt.Sprintf("%d", opts.Config.Parallel),
		"--label", ContainerLabel + "=true",
		"--label", ProjectLabel + "=" + opts.ProjectDir,
		"--hostname", "reviewbot",
	}
	if opts.BaseBranch != "" {
		args = append(args, "-e", "REVIEWBOT_BASE="+opts.BaseBranch)
	}
	if opts.Personality != "" {
		args = append(args, "-e", "REVIEWBOT_PERSONALITY="+opts.Personality)
	}
	if opts.Staged {
		args = append(args, "-e", "REVIEWBOT_STAGED=1")
	}
	if opts.SinceN > 0 {
		args = append(args, "-e", fmt.Sprintf("REVIEWBOT_SINCE=%d", opts.SinceN))
	}
	if len(opts.Config.SkipAgents) > 0 {
		args = append(args, "-e", "REVIEWBOT_SKIP_AGENTS="+strings.Join(opts.Config.SkipAgents, ","))
	}
	for k, v := range opts.Config.ExtraEnv {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, imageName)

	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker run: %w\n%s", err, string(out))
	}
	return nil
}

// StreamLogs tails container logs to the given writer until the container
// exits or ctx is cancelled.
func StreamLogs(name string, w *os.File) error {
	cmd := exec.Command("docker", "logs", "-f", name)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// IsRunning reports whether the container is still up.
func IsRunning(name string) bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Wait blocks until the container exits.
func Wait(name string) {
	exec.Command("docker", "wait", name).Run()
}

// Attach attaches to the container's tmux session interactively. After the
// orchestrator finishes, the container hosts a "reviewbot" tmux session
// running a Claude follow-up chat.
func Attach(name string) error {
	uid, _, _ := getHostUserInfo()
	cmd := exec.Command("docker", "exec", "-it", "-u", uid, name,
		"bash", "-lc", "tmux attach-session -t reviewbot")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// HasTmux returns true if the orchestrator has reached the follow-up tmux
// session (i.e. the review is complete).
func HasTmux(name string) bool {
	uid, _, _ := getHostUserInfo()
	cmd := exec.Command("docker", "exec", "-u", uid, name,
		"tmux", "has-session", "-t", "reviewbot")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// ListContainers returns all reviewbot containers.
func ListContainers() ([]ContainerInfo, error) {
	cmd := exec.Command("docker", "ps", "-a",
		"--filter", "label="+ContainerLabel,
		"--format", "{{json .}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var infos []ContainerInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var c struct {
			Names     string `json:"Names"`
			ID        string `json:"ID"`
			State     string `json:"State"`
			Labels    string `json:"Labels"`
			CreatedAt string `json:"CreatedAt"`
		}
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue
		}
		project := ""
		for _, label := range strings.Split(c.Labels, ",") {
			if strings.HasPrefix(label, ProjectLabel+"=") {
				project = strings.TrimPrefix(label, ProjectLabel+"=")
				break
			}
		}
		infos = append(infos, ContainerInfo{
			Name: c.Names, ID: c.ID, State: c.State,
			ProjectDir: project, Created: c.CreatedAt,
		})
	}
	return infos, nil
}

// ContainerInfo is the host-side view of a reviewbot container.
type ContainerInfo struct {
	Name       string
	ID         string
	State      string
	ProjectDir string
	Created    string
}

// Destroy stops + removes a container.
func Destroy(name string) {
	exec.Command("docker", "stop", "--time", "2", name).Run()
	exec.Command("docker", "rm", "-f", name).Run()
}

// DestroyAll stops + removes every reviewbot container. Returns count.
func DestroyAll() (int, error) {
	cs, err := ListContainers()
	if err != nil {
		return 0, err
	}
	for _, c := range cs {
		Destroy(c.Name)
	}
	return len(cs), nil
}

// WaitUntilReady polls docker until the container's state.Running is true
// (or until timeout). Used right after RunDetached to give the orchestrator
// a moment before we start streaming.
func WaitUntilReady(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if IsRunning(name) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("container %s did not start within %s", name, timeout)
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
