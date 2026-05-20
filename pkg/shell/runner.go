package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Result struct {
	Command  string        `json:"command"`
	Args     []string      `json:"args"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	ExitCode int           `json:"exitCode"`
	Duration time.Duration `json:"duration"`
}

type Options struct {
	UseSudo            bool
	Stdin              string
	LogStdoutOnSuccess bool
	LogStderrOnSuccess bool
}

var preferredCommandPaths = map[string][]string{
	"lsblk":      {"/usr/bin/lsblk"},
	"smartctl":   {"/usr/sbin/smartctl", "/sbin/smartctl"},
	"mdadm":      {"/usr/sbin/mdadm", "/sbin/mdadm"},
	"wipefs":     {"/usr/sbin/wipefs", "/sbin/wipefs"},
	"blkid":      {"/usr/sbin/blkid", "/sbin/blkid"},
	"mount":      {"/usr/bin/mount", "/bin/mount"},
	"umount":     {"/usr/bin/umount", "/bin/umount"},
	"tee":        {"/usr/bin/tee", "/bin/tee"},
	"mkdir":      {"/usr/bin/mkdir", "/bin/mkdir"},
	"rm":         {"/usr/bin/rm", "/bin/rm"},
	"mv":         {"/usr/bin/mv", "/bin/mv"},
	"cp":         {"/usr/bin/cp", "/bin/cp"},
	"chmod":      {"/usr/bin/chmod", "/bin/chmod"},
	"touch":      {"/usr/bin/touch", "/bin/touch"},
	"dd":         {"/usr/bin/dd", "/bin/dd"},
	"sync":       {"/usr/bin/sync", "/bin/sync"},
	"rclone":     {"/usr/bin/rclone", "/usr/local/bin/rclone", "/bin/rclone"},
	"mkfs.btrfs": {"/usr/sbin/mkfs.btrfs", "/sbin/mkfs.btrfs", "/usr/bin/mkfs.btrfs"},
	"btrfs":      {"/usr/bin/btrfs", "/bin/btrfs"},
}

func Run(ctx context.Context, command string, args ...string) (Result, error) {
	return RunWithOptions(ctx, Options{}, command, args...)
}

func RunWithOptions(ctx context.Context, opts Options, command string, args ...string) (Result, error) {
	start := time.Now()
	execCommand := command
	execArgs := append([]string(nil), args...)
	if opts.UseSudo {
		resolvedCommand, err := resolveCommandPath(command)
		if err != nil {
			return Result{}, fmt.Errorf("resolve command %s: %w", command, err)
		}
		execArgs = append([]string{resolvedCommand}, execArgs...)
		execCommand = "sudo"
	}
	cmd := exec.CommandContext(ctx, execCommand, execArgs...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}

	log.Printf("[CMD] start: %s", renderCommand(execCommand, execArgs))
	err := cmd.Run()
	duration := time.Since(start)

	result := Result{
		Command:  execCommand,
		Args:     append([]string(nil), execArgs...),
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
		ExitCode: exitCode(err),
	}

	log.Printf("[CMD] done: %s exit=%d duration=%s", renderCommand(execCommand, execArgs), result.ExitCode, duration)
	stdoutText := strings.TrimSpace(result.Stdout)
	stderrText := strings.TrimSpace(result.Stderr)
	if err != nil {
		if stdoutText != "" {
			log.Printf("[CMD] stdout: %s", stdoutText)
		}
		if stderrText != "" {
			log.Printf("[CMD] stderr: %s", stderrText)
		}
	} else {
		if stdoutText != "" {
			if opts.LogStdoutOnSuccess {
				log.Printf("[CMD] stdout: %s", stdoutText)
			} else {
				log.Printf("[CMD] stdout: %s", summarizeOutput(stdoutText))
			}
		}
		if stderrText != "" {
			if opts.LogStderrOnSuccess {
				log.Printf("[CMD] stderr: %s", stderrText)
			} else {
				log.Printf("[CMD] stderr: %s", summarizeOutput(stderrText))
			}
		}
	}

	if err != nil {
		return result, fmt.Errorf("run command %s: %w", renderCommand(execCommand, execArgs), err)
	}
	return result, nil
}

func resolveCommandPath(command string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("empty command")
	}
	if strings.HasPrefix(command, "/") {
		return command, nil
	}
	if candidates, ok := preferredCommandPaths[command]; ok {
		for _, candidate := range candidates {
			if fileInfo, err := os.Stat(candidate); err == nil && !fileInfo.IsDir() {
				return candidate, nil
			}
		}
	}
	return exec.LookPath(command)
}

func renderCommand(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(command))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n'\"\\$`") {
		return value
	}
	return strconv.Quote(value)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
		return exitErr.ProcessState.ExitCode()
	}
	return -1
}

func summarizeOutput(value string) string {
	lines := strings.Count(value, "\n") + 1
	if len(value) <= 160 && lines <= 3 {
		return value
	}
	preview := value
	if newline := strings.IndexByte(preview, '\n'); newline >= 0 {
		preview = preview[:newline]
	}
	if len(preview) > 120 {
		preview = preview[:120]
	}
	return fmt.Sprintf("%d bytes across %d lines; first line: %s", len(value), lines, preview)
}
