package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type PullImageStreamOptions struct {
	Command string
}

func PullImageStream(ctx context.Context, opts PullImageStreamOptions, emit func(ImagePullEvent) bool) error {
	imageRef, err := parsePullCommand(opts.Command)
	if err != nil {
		return err
	}
	now := func() string { return time.Now().UTC().Format(time.RFC3339) }
	var emitMu sync.Mutex
	safeEmit := func(event ImagePullEvent) bool {
		emitMu.Lock()
		defer emitMu.Unlock()
		return emit(event)
	}

	safeEmit(ImagePullEvent{
		Stage:     "started",
		Message:   "开始拉取 " + imageRef,
		ImageRef:  imageRef,
		Percent:   3,
		UpdatedAt: now(),
	})

	dockerPath, err := findDockerPath()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "sudo", dockerPath, "pull", imageRef)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open docker pull stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open docker pull stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start docker pull: %w", err)
	}

	var wg sync.WaitGroup
	progress := newPullProgress()
	readPipe := func(reader io.Reader) {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			if !safeEmit(ImagePullEvent{
				Stage:     "running",
				Message:   line,
				ImageRef:  imageRef,
				Percent:   progress.next(line),
				UpdatedAt: now(),
			}) {
				return
			}
		}
	}
	wg.Add(2)
	go readPipe(stdout)
	go readPipe(stderr)
	wg.Wait()

	err = cmd.Wait()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
			exitCode = exitErr.ProcessState.ExitCode()
		}
		safeEmit(ImagePullEvent{
			Stage:     "failed",
			Message:   "镜像拉取失败",
			ImageRef:  imageRef,
			Percent:   progress.current(),
			ExitCode:  exitCode,
			UpdatedAt: now(),
		})
		return fmt.Errorf("docker pull %s failed: %w", imageRef, err)
	}

	icon := ResolveImageIcon(ctx, imageRef)
	if err := upsertImageMetadata(imageRef, icon, time.Now().UTC()); err != nil {
		safeEmit(ImagePullEvent{
			Stage:     "warning",
			Message:   "镜像已拉取，但图标元数据保存失败：" + err.Error(),
			ImageRef:  imageRef,
			Percent:   98,
			UpdatedAt: now(),
		})
	} else if icon != defaultImageIcon {
		safeEmit(ImagePullEvent{
			Stage:     "running",
			Message:   "已匹配镜像图标",
			ImageRef:  imageRef,
			Percent:   99,
			UpdatedAt: now(),
		})
	}

	safeEmit(ImagePullEvent{
		Stage:     "completed",
		Message:   "镜像拉取完成",
		ImageRef:  imageRef,
		Percent:   100,
		ExitCode:  0,
		UpdatedAt: now(),
	})
	return nil
}

func parsePullCommand(command string) (string, error) {
	parts := strings.Fields(strings.TrimSpace(command))
	if len(parts) == 0 {
		return "", fmt.Errorf("请输入镜像名或 docker pull 命令")
	}
	if parts[0] == "sudo" {
		parts = parts[1:]
	}
	if len(parts) >= 2 && parts[0] == "docker" && parts[1] == "pull" {
		parts = parts[2:]
	} else if parts[0] == "pull" {
		parts = parts[1:]
	}
	if len(parts) != 1 {
		return "", fmt.Errorf("只支持单个镜像的 docker pull 命令")
	}
	imageRef := strings.TrimSpace(parts[0])
	if imageRef == "" || strings.HasPrefix(imageRef, "-") || strings.ContainsAny(imageRef, ";&|`$<>") {
		return "", fmt.Errorf("镜像名称不合法")
	}
	return imageRef, nil
}

func findDockerPath() (string, error) {
	candidates := []string{"/usr/sbin/docker", "/usr/bin/docker", "/usr/local/bin/docker", "/bin/docker"}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return exec.LookPath("docker")
}

type pullProgress struct {
	mu      sync.Mutex
	percent float64
}

func newPullProgress() *pullProgress {
	return &pullProgress{percent: 8}
}

func (p *pullProgress) next(line string) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, "pulling fs layer"):
		p.percent = maxFloat64(p.percent, 18)
	case strings.Contains(lower, "downloading"):
		p.percent = minFloat64(maxFloat64(p.percent+2.5, 24), 78)
	case strings.Contains(lower, "extracting"):
		p.percent = minFloat64(maxFloat64(p.percent+2, 72), 92)
	case strings.Contains(lower, "pull complete"):
		p.percent = minFloat64(maxFloat64(p.percent+3, 85), 96)
	case strings.Contains(lower, "digest:"):
		p.percent = maxFloat64(p.percent, 97)
	case strings.Contains(lower, "downloaded newer image") || strings.Contains(lower, "image is up to date"):
		p.percent = maxFloat64(p.percent, 98)
	default:
		p.percent = minFloat64(p.percent+0.8, 88)
	}
	return p.percent
}

func (p *pullProgress) current() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.percent
}

func minFloat64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
