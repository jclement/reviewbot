// Package ui provides terminal output formatting for reviewbot.
// Handles progress bars, status messages, and styled output.
// Respects TTY detection — when piped, falls back to plain text.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// Colors for terminal output.
const (
	Red     = "\033[0;31m"
	Green   = "\033[0;32m"
	Yellow  = "\033[0;33m"
	Blue    = "\033[0;34m"
	Magenta = "\033[0;35m"
	Cyan    = "\033[0;36m"
	White   = "\033[1;37m"
	Gray    = "\033[0;37m"
	Bold    = "\033[1m"
	Dim     = "\033[2m"
	Reset   = "\033[0m"
)

// Writer handles formatted output to a writer (usually os.Stderr).
type Writer struct {
	out   io.Writer
	isTTY bool
}

// NewWriter creates a Writer that auto-detects TTY for the given file.
func NewWriter(f *os.File) *Writer {
	return &Writer{
		out:   f,
		isTTY: term.IsTerminal(int(f.Fd())),
	}
}

// Stderr returns a Writer connected to os.Stderr.
func Stderr() *Writer {
	return NewWriter(os.Stderr)
}

// Banner prints the reviewbot ASCII banner.
func (w *Writer) Banner() {
	if !w.isTTY {
		fmt.Fprintln(w.out, "=== reviewbot ===")
		return
	}
	fmt.Fprintln(w.out)
	fmt.Fprintf(w.out, "%s  ┏━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┓%s\n", Magenta, Reset)
	fmt.Fprintf(w.out, "%s  ┃%s         %s🤖  R E V I E W B O T  🤖%s             %s┃%s\n", Magenta, Reset, White, Reset, Magenta, Reset)
	fmt.Fprintf(w.out, "%s  ┃%s      %sAgentic code review on your branch%s       %s┃%s\n", Magenta, Reset, Gray, Reset, Magenta, Reset)
	fmt.Fprintf(w.out, "%s  ┗━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━┛%s\n", Magenta, Reset)
	fmt.Fprintln(w.out)
}

// Info prints an informational message.
func (w *Writer) Info(msg string) {
	if w.isTTY {
		fmt.Fprintf(w.out, "  %s▸%s %s\n", Cyan, Reset, msg)
	} else {
		fmt.Fprintf(w.out, "  > %s\n", msg)
	}
}

// Success prints a success message.
func (w *Writer) Success(msg string) {
	if w.isTTY {
		fmt.Fprintf(w.out, "  %s✔%s %s\n", Green, Reset, msg)
	} else {
		fmt.Fprintf(w.out, "  [OK] %s\n", msg)
	}
}

// Warn prints a warning message.
func (w *Writer) Warn(msg string) {
	if w.isTTY {
		fmt.Fprintf(w.out, "  %s⚠%s %s\n", Yellow, Reset, msg)
	} else {
		fmt.Fprintf(w.out, "  [WARN] %s\n", msg)
	}
}

// Error prints an error message.
func (w *Writer) Error(msg string) {
	if w.isTTY {
		fmt.Fprintf(w.out, "  %s✖%s %s\n", Red, Reset, msg)
	} else {
		fmt.Fprintf(w.out, "  [ERROR] %s\n", msg)
	}
}

// Step prints a section step header.
func (w *Writer) Step(msg string) {
	if w.isTTY {
		fmt.Fprintf(w.out, "  %s◆%s %s%s%s\n", Blue, Reset, Bold, msg, Reset)
	} else {
		fmt.Fprintf(w.out, "  * %s\n", msg)
	}
}

// Detail prints an indented detail line.
func (w *Writer) Detail(msg string) {
	if w.isTTY {
		fmt.Fprintf(w.out, "    %s→%s %s\n", Dim, Reset, msg)
	} else {
		fmt.Fprintf(w.out, "    -> %s\n", msg)
	}
}

// Separator prints a horizontal rule.
func (w *Writer) Separator() {
	if w.isTTY {
		fmt.Fprintf(w.out, "\n  %s━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━%s\n\n", Dim, Reset)
	} else {
		fmt.Fprintf(w.out, "\n  ---\n\n")
	}
}

// Progress renders a progress bar with status text.
func (w *Writer) Progress(pct float64, status string) {
	if !w.isTTY {
		fmt.Fprintf(w.out, "[%3.0f%%] %s\n", pct*100, status)
		return
	}

	width := 30
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}
	empty := width - filled

	bar := Green + strings.Repeat("█", filled) + Dim + strings.Repeat("░", empty) + Reset

	fmt.Fprintf(w.out, "\r%s[%s]%s %s%-40s%s", Dim, bar, Reset, Cyan, status, Reset)

	if pct >= 1.0 {
		fmt.Fprintln(w.out)
	}
}

// Blank prints an empty line.
func (w *Writer) Blank() {
	fmt.Fprintln(w.out)
}

// IsTTY returns whether the writer is connected to a terminal.
func (w *Writer) IsTTY() bool {
	return w.isTTY
}

// StatusFlair returns a random fun status word.
var statusFlair = []string{
	"Spinning up sandboxes",
	"Wrangling Docker containers",
	"Summoning Claude workers",
	"Bootstrapping the future",
	"Building your dev universe",
	"Containerizing creativity",
	"Preparing for autonomous chaos",
	"Loading AI superpowers",
	"Compiling your ambitions",
	"Initializing boldness protocol",
}

// RandomFlair returns a random sarcastic status message.
func RandomFlair() string {
	return statusFlair[time.Now().UnixNano()%int64(len(statusFlair))]
}
