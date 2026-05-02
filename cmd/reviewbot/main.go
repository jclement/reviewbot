// reviewbot — agentic code review for the current branch.
//
// Usage:
//
//	reviewbot                  # review current dir vs detected parent branch
//	reviewbot <path>           # review a specific project
//	reviewbot --base develop   # override the parent branch
//	reviewbot chat             # re-attach to the post-review tmux for follow-ups
//	reviewbot list             # show running review containers
//	reviewbot stop             # stop / clean up review containers
//	reviewbot doctor           # check Docker + image health
//	reviewbot init             # scaffold .reviewbot/ in the current project
package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jclement/reviewbot/internal/config"
	"github.com/jclement/reviewbot/internal/docker"
	"github.com/jclement/reviewbot/internal/report"
	"github.com/jclement/reviewbot/internal/ui"
	"github.com/spf13/cobra"
)

var version = "dev"

func main() {
	w := ui.Stderr()

	rootCmd := &cobra.Command{
		Use:           "reviewbot [path]",
		Short:         "Agentic code review for the current branch",
		Long:          longHelp,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReview(cmd, args, w)
		},
	}
	rootCmd.Version = version
	rootCmd.SetVersionTemplate("reviewbot " + version + "\n")
	rootCmd.Flags().String("base", "", "parent branch to compare against (default: auto-detect)")
	rootCmd.Flags().String("arch", docker.DefaultArch(), "container CPU architecture")
	rootCmd.Flags().Bool("rebuild", false, "force rebuild of the docker image")
	rootCmd.Flags().Bool("no-browser", false, "don't open the browser automatically")
	rootCmd.Flags().Bool("no-chat", false, "skip the post-review tmux follow-up chat")
	rootCmd.Flags().String("personality", "", "tone for summaries + report theme: sexy | angry | sarcastic | butler")
	rootCmd.Flags().Bool("staged", false, "review staged (uncommitted) changes instead of branch diff")
	rootCmd.Flags().Int("since", 0, "review the last N commits instead of branch diff (mutually exclusive with --staged/--base)")

	rootCmd.AddCommand(chatCmd(w))
	rootCmd.AddCommand(listCmd(w))
	rootCmd.AddCommand(stopCmd(w))
	rootCmd.AddCommand(doctorCmd(w))
	rootCmd.AddCommand(initCmd(w))
	rootCmd.AddCommand(cleanCmd(w))

	if err := rootCmd.Execute(); err != nil {
		w.Error(err.Error())
		os.Exit(1)
	}
}

const longHelp = `reviewbot — agentic code review for the current branch.

Spins up a Docker container that runs 16+ specialist Claude reviewer agents
in parallel against the diff between your current branch and its parent
(auto-detected via origin/HEAD or main/master/develop). Renders a live
HTML report and opens it in your browser; the page auto-refreshes as each
agent completes.

When the review is done, drops you into a tmux follow-up chat with Claude
loaded with the full context, so you can ask "explain finding 3 in more
detail" or "draft a fix for the SQL-injection one."`

// ── review (default command) ─────────────────────────────────────────────

func runReview(cmd *cobra.Command, args []string, w *ui.Writer) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	projectDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	info, err := os.Stat(projectDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("not a directory: %s", projectDir)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil {
		return fmt.Errorf("not a git repository: %s", projectDir)
	}

	cfg := config.Load(projectDir)
	baseFlag, _ := cmd.Flags().GetString("base")
	if baseFlag != "" {
		cfg.BaseBranch = baseFlag
	}
	arch, _ := cmd.Flags().GetString("arch")
	rebuild, _ := cmd.Flags().GetBool("rebuild")
	noBrowser, _ := cmd.Flags().GetBool("no-browser")
	noChat, _ := cmd.Flags().GetBool("no-chat")
	personality, _ := cmd.Flags().GetString("personality")
	personality = strings.ToLower(strings.TrimSpace(personality))
	switch personality {
	case "", "sexy", "angry", "sarcastic", "butler":
		// valid (empty = neutral default)
	default:
		return fmt.Errorf("invalid --personality %q (choose: sexy, angry, sarcastic, butler, or omit)", personality)
	}
	staged, _ := cmd.Flags().GetBool("staged")
	since, _ := cmd.Flags().GetInt("since")
	// Validate the diff-source flags. They're mutually exclusive.
	modeFlags := 0
	if staged {
		modeFlags++
	}
	if since > 0 {
		modeFlags++
	}
	if cfg.BaseBranch != "" && (staged || since > 0) {
		return fmt.Errorf("--base is mutually exclusive with --staged / --since")
	}
	if modeFlags > 1 {
		return fmt.Errorf("--staged and --since are mutually exclusive")
	}
	if since < 0 {
		return fmt.Errorf("--since must be a positive integer")
	}

	w.Banner()

	if !docker.IsDockerAvailable() {
		return fmt.Errorf("docker is not available — install Docker Desktop or the Docker engine")
	}
	w.Info(fmt.Sprintf("Project: %s%s%s", ui.Bold, projectDir, ui.Reset))
	switch {
	case staged:
		w.Info(fmt.Sprintf("Mode:    %sstaged changes%s (uncommitted, in the index)", ui.Cyan, ui.Reset))
	case since > 0:
		w.Info(fmt.Sprintf("Mode:    %slast %d commits%s", ui.Cyan, since, ui.Reset))
	case cfg.BaseBranch != "":
		w.Info(fmt.Sprintf("Base:    %s%s%s (override)", ui.Cyan, cfg.BaseBranch, ui.Reset))
	default:
		w.Info(fmt.Sprintf("Base:    %sauto-detect%s", ui.Dim, ui.Reset))
	}

	// ── Build / refresh image ─────────────────────────────────────────
	if rebuild {
		w.Step("Forcing rebuild")
		_ = docker.Remove(cfg)
		if err := buildImage(cfg, arch, w); err != nil {
			return err
		}
	} else {
		status, detail, err := docker.Inspect(cfg)
		if err != nil {
			return err
		}
		switch status {
		case docker.StatusReady:
			w.Success(fmt.Sprintf("Image %s%s:%s%s ready", ui.Bold, cfg.Image, cfg.Tag, ui.Reset))
		case docker.StatusOutdated:
			w.Info(fmt.Sprintf("Image outdated (%s) — rebuilding", detail))
			if err := buildImage(cfg, arch, w); err != nil {
				return err
			}
		case docker.StatusMissing:
			w.Info("First run — building image")
			if err := buildImage(cfg, arch, w); err != nil {
				return err
			}
		}
	}

	// ── Home volume (caches the Claude install) ───────────────────────
	if !docker.VolumeExists(cfg) {
		if err := docker.CreateVolume(cfg); err != nil {
			return fmt.Errorf("creating volume: %w", err)
		}
		w.Info("Created volume " + cfg.HomeVolume + " (first run will install Claude Code)")
	}

	// ── Output dir + HTTP server ──────────────────────────────────────
	// We deliberately put this under ~/.cache (or %LOCALAPPDATA% on Windows)
	// rather than os.TempDir(): on macOS, $TMPDIR points into /var/folders
	// which isn't always exposed to Docker Desktop's bind-mount sharing,
	// so the container writes index.html and the host can never see it.
	// The user's home directory is universally shared.
	runID := newRunID()
	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		// Fallback: home dir.
		if h, err2 := os.UserHomeDir(); err2 == nil {
			cacheRoot = filepath.Join(h, ".cache")
		} else {
			return fmt.Errorf("locating cache dir: %w", err)
		}
	}
	outDir := filepath.Join(cacheRoot, "reviewbot", "runs", runID)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	srv, err := report.New(outDir)
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil {
		return fmt.Errorf("starting report server: %w", err)
	}
	defer srv.Shutdown()
	w.Info(fmt.Sprintf("Report:  %s%s%s", ui.Cyan, srv.URL(), ui.Reset))
	w.Info(fmt.Sprintf("Output:  %s%s%s", ui.Dim, outDir, ui.Reset))

	// ── Launch container ──────────────────────────────────────────────
	containerName := "reviewbot-" + runID
	w.Step("Launching reviewers")
	err = docker.RunDetached(docker.RunOptions{
		Name:        containerName,
		ProjectDir:  projectDir,
		OutputDir:   outDir,
		Config:      cfg,
		Arch:        arch,
		BaseBranch:  cfg.BaseBranch,
		Personality: personality,
		Staged:      staged,
		SinceN:      since,
	})
	if err != nil {
		return err
	}

	// Make sure the container is cleaned up on Ctrl-C while still running.
	sigCtx, cancel := signalContext()
	defer cancel()
	go func() {
		<-sigCtx.Done()
		// If the orchestrator hasn't reached the tmux phase yet, tear it
		// down. Once tmux is up the user can re-attach with `reviewbot chat`.
		if !docker.HasTmux(containerName) {
			docker.Destroy(containerName)
		}
	}()

	if err := docker.WaitUntilReady(containerName, 30*time.Second); err != nil {
		return err
	}
	w.Success("Container running: " + containerName)

	// ── Open browser as soon as index.html appears ────────────────────
	go func() {
		// Poll up to 5 minutes — generous because slow Docker bind mounts
		// (mac VirtioFS, gRPC-FUSE) can lag behind container writes by
		// several seconds. We also bail early if the container dies.
		deadline := time.Now().Add(5 * time.Minute)
		indexPath := filepath.Join(outDir, "index.html")
		for time.Now().Before(deadline) {
			if !docker.IsRunning(containerName) {
				w.Warn("Container exited before report appeared — see logs above")
				return
			}
			if _, err := os.Stat(indexPath); err == nil {
				if noBrowser {
					w.Info("Report ready at " + srv.URL() + " (browser auto-open disabled)")
				} else if err := report.OpenBrowser(srv.URL()); err != nil {
					w.Warn("Could not open browser automatically: " + err.Error())
					w.Info("Open this URL manually: " + srv.URL())
				} else {
					w.Success("Opened report in browser: " + srv.URL())
				}
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
		w.Warn("Timed out waiting for report. Open " + srv.URL() + " manually if it appears.")
	}()

	// Stream container logs to stderr so the user sees orchestrator progress.
	w.Separator()
	w.Info("Streaming reviewer progress (the browser tab will auto-update)")
	w.Blank()
	go docker.StreamLogs(containerName, os.Stderr)

	// ── Wait for the orchestrator to reach the tmux phase ─────────────
	tmuxReady := waitForTmux(containerName, 60*time.Minute)
	if !tmuxReady {
		w.Warn("Container exited before reaching the follow-up chat phase")
		return nil
	}

	w.Blank()
	w.Separator()
	w.Success("Review complete — report is live at " + srv.URL())
	w.Blank()

	if noChat {
		w.Info("Press Ctrl-C to stop the report server.")
		<-sigCtx.Done()
		docker.Destroy(containerName)
		return nil
	}

	// ── Drop the user into the follow-up tmux chat ───────────────────
	w.Step("Attaching to follow-up chat (Ctrl-B then D to detach, exit to end)")
	w.Blank()
	if err := docker.Attach(containerName); err != nil {
		w.Warn("Could not attach to follow-up chat: " + err.Error())
	}

	time.Sleep(2 * time.Second)
	if !docker.IsRunning(containerName) {
		docker.Destroy(containerName)
	} else {
		w.Info(fmt.Sprintf("Chat detached. Container %s%s%s still running — `reviewbot chat` to re-attach.",
			ui.Bold, containerName, ui.Reset))
	}
	return nil
}

// ── chat ─────────────────────────────────────────────────────────────────

func chatCmd(w *ui.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "chat [container]",
		Short: "Re-attach to a finished review's follow-up chat",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cs, err := docker.ListContainers()
			if err != nil {
				return err
			}
			var target string
			if len(args) > 0 {
				target = args[0]
			} else if len(cs) == 0 {
				return fmt.Errorf("no review containers — run `reviewbot` first")
			} else if len(cs) == 1 {
				target = cs[0].Name
			} else {
				w.Info("Multiple review containers — pick one with `reviewbot chat <name>`:")
				for _, c := range cs {
					fmt.Fprintf(os.Stderr, "  %s  %s\n", c.Name, c.ProjectDir)
				}
				return nil
			}
			if !docker.HasTmux(target) {
				return fmt.Errorf("container %s isn't ready for chat yet (review still in progress?)", target)
			}
			return docker.Attach(target)
		},
	}
}

// ── list / stop / clean / doctor / init ────────────────────────────────

func listCmd(w *ui.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active review containers",
		RunE: func(_ *cobra.Command, _ []string) error {
			cs, err := docker.ListContainers()
			if err != nil {
				return err
			}
			if len(cs) == 0 {
				w.Info("No review containers")
				return nil
			}
			for _, c := range cs {
				dot, color := "●", ui.Green
				if c.State != "running" {
					dot, color = "○", ui.Yellow
				}
				fmt.Fprintf(os.Stderr, "  %s%s%s  %s%-30s%s  %s%s%s  %s\n",
					color, dot, ui.Reset,
					ui.Bold, c.Name, ui.Reset,
					ui.Dim, c.State, ui.Reset,
					c.ProjectDir)
			}
			return nil
		},
	}
}

func stopCmd(w *ui.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "stop [container]",
		Short: "Stop and remove review containers (all if name omitted)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				docker.Destroy(args[0])
				w.Success("Stopped " + args[0])
				return nil
			}
			n, err := docker.DestroyAll()
			if err != nil {
				return err
			}
			w.Success(fmt.Sprintf("Stopped %d container(s)", n))
			return nil
		},
	}
}

func cleanCmd(w *ui.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "clean",
		Short: "Remove the docker image and home volume",
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg := config.DefaultConfig()
			n, _ := docker.DestroyAll()
			if n > 0 {
				w.Info(fmt.Sprintf("Stopped %d container(s)", n))
			}
			if docker.VolumeExists(cfg) {
				if err := docker.RemoveVolume(cfg); err != nil {
					w.Warn("could not remove volume: " + err.Error())
				} else {
					w.Success("Removed volume " + cfg.HomeVolume)
				}
			}
			status, _, _ := docker.Inspect(cfg)
			if status != docker.StatusMissing {
				if err := docker.Remove(cfg); err != nil {
					w.Warn("could not remove image: " + err.Error())
				} else {
					w.Success("Removed image " + cfg.Image + ":" + cfg.Tag)
				}
			}
			return nil
		},
	}
}

func doctorCmd(w *ui.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check Docker + image health",
		RunE: func(_ *cobra.Command, _ []string) error {
			w.Banner()
			cfg := config.DefaultConfig()
			if !docker.IsDockerAvailable() {
				w.Error("Docker is not available — install Docker Desktop or the Docker engine")
				os.Exit(1)
			}
			w.Success("Docker is installed and running")
			status, detail, _ := docker.Inspect(cfg)
			switch status {
			case docker.StatusReady:
				w.Success("Image " + cfg.Image + ":" + cfg.Tag + " is up to date")
			case docker.StatusOutdated:
				w.Warn("Image needs rebuild: " + detail)
			case docker.StatusMissing:
				w.Info("Image not built yet (will build on first run)")
			}
			if docker.VolumeExists(cfg) {
				w.Success("Volume " + cfg.HomeVolume + " exists")
			} else {
				w.Info("Volume not created yet")
			}
			cs, _ := docker.ListContainers()
			running := 0
			for _, c := range cs {
				if c.State == "running" {
					running++
				}
			}
			w.Info(fmt.Sprintf("Containers: %d total, %d running", len(cs), running))
			return nil
		},
	}
}

func initCmd(w *ui.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "init [path]",
		Short: "Create .reviewbot/ in the project (config + project rules)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			cfgDir, err := config.Init(abs)
			if err != nil {
				return err
			}
			w.Success("Created " + cfgDir)
			w.Info("Edit " + cfgDir + "/config.yaml to configure")
			w.Info("Edit " + cfgDir + "/CLAUDE.md to add project-specific review rules")
			return nil
		},
	}
}

// ── helpers ──────────────────────────────────────────────────────────────

func buildImage(cfg *config.Config, arch string, w *ui.Writer) error {
	w.Step("Building reviewbot image (linux/" + arch + ")")
	err := docker.Build(cfg, arch, func(line string) {
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "Step") {
			w.Detail(line)
		}
	})
	if err != nil {
		return fmt.Errorf("image build failed: %w", err)
	}
	w.Success("Image built")
	return nil
}

// waitForTmux polls until the orchestrator's follow-up tmux session exists
// (= review is complete and chat is ready). False on timeout / container exit.
func waitForTmux(name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !docker.IsRunning(name) {
			return false
		}
		if docker.HasTmux(name) {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// signalContext returns a context cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}

func newRunID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 4)
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(b)
}
