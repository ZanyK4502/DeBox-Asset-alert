package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultSSHPath      = "ssh"
	defaultLocalAddress = "127.0.0.1:8000"
	defaultDataDir      = "data"
	startupTimeout      = 45 * time.Second
	startupPollInterval = 2 * time.Second
	healthInterval      = 30 * time.Second
	restartDelay        = 3 * time.Second
)

var publicURLPattern = regexp.MustCompile(`https://[a-z0-9-]+\.lhr\.life`)

type Config struct {
	SSHPath      string
	LocalAddress string
	DataDir      string
}

func DefaultConfig() Config {
	return Config{
		SSHPath:      defaultSSHPath,
		LocalAddress: defaultLocalAddress,
		DataDir:      defaultDataDir,
	}
}

type Manager struct {
	config Config
	client *http.Client
}

func New(config Config) *Manager {
	if strings.TrimSpace(config.SSHPath) == "" {
		config.SSHPath = defaultSSHPath
	}
	if strings.TrimSpace(config.LocalAddress) == "" {
		config.LocalAddress = defaultLocalAddress
	}
	if strings.TrimSpace(config.DataDir) == "" {
		config.DataDir = defaultDataDir
	}
	return &Manager{
		config: config,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (m *Manager) Run(ctx context.Context, logger *slog.Logger) error {
	if _, err := exec.LookPath(m.config.SSHPath); err != nil {
		return fmt.Errorf("find SSH executable %q: %w", m.config.SSHPath, err)
	}
	if err := os.MkdirAll(m.config.DataDir, 0o755); err != nil {
		return fmt.Errorf("create tunnel data directory: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := m.runTunnel(ctx, logger); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("tunnel stopped", "error", err)
		}
		if !waitForContext(ctx, restartDelay) {
			return nil
		}
	}
}

func (m *Manager) runTunnel(ctx context.Context, logger *slog.Logger) error {
	currentLogPath := filepath.Join(m.config.DataDir, "tunnel-current.log")
	output, err := os.OpenFile(currentLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open tunnel output log: %w", err)
	}
	defer output.Close()

	command := exec.CommandContext(
		ctx,
		m.config.SSHPath,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
		"-o", "ExitOnForwardFailure=yes",
		"-R", "80:"+m.config.LocalAddress,
		"nokey@localhost.run",
	)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return fmt.Errorf("start tunnel: %w", err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- command.Wait()
	}()

	url, err := m.waitForURL(ctx, currentLogPath, exited)
	if err != nil {
		stopProcess(command)
		return err
	}
	if err := m.publishURL(url); err != nil {
		stopProcess(command)
		return err
	}
	logger.Info("tunnel ready", "url", url)

	failures := 0
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			stopProcess(command)
			return nil
		case waitErr := <-exited:
			if waitErr == nil {
				return errors.New("tunnel process exited")
			}
			return fmt.Errorf("tunnel process exited: %w", waitErr)
		case <-ticker.C:
			if m.healthy(ctx, url) {
				failures = 0
				continue
			}
			failures++
			logger.Warn("tunnel health check failed", "url", url, "failures", failures)
			if failures >= 2 {
				stopProcess(command)
				return errors.New("tunnel failed two consecutive health checks")
			}
		}
	}
}

func (m *Manager) waitForURL(
	ctx context.Context,
	logPath string,
	exited <-chan error,
) (string, error) {
	deadline := time.NewTimer(startupTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(startupPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-exited:
			if err == nil {
				return "", errors.New("tunnel exited before publishing a URL")
			}
			return "", fmt.Errorf("tunnel exited before publishing a URL: %w", err)
		case <-deadline.C:
			return "", errors.New("tunnel did not become healthy before the startup timeout")
		case <-ticker.C:
			body, err := os.ReadFile(logPath)
			if err != nil {
				continue
			}
			url := ExtractPublicURL(string(body))
			if url != "" && m.healthy(ctx, url) {
				return url, nil
			}
		}
	}
}

func (m *Manager) healthy(ctx context.Context, publicURL string) bool {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(publicURL, "/")+"/api/health",
		nil,
	)
	if err != nil {
		return false
	}
	response, err := m.client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode == http.StatusOK
}

func (m *Manager) publishURL(publicURL string) error {
	path := filepath.Join(m.config.DataDir, "public_url.txt")
	if err := os.WriteFile(path, []byte(publicURL), 0o600); err != nil {
		return fmt.Errorf("publish tunnel URL: %w", err)
	}
	return nil
}

func ExtractPublicURL(output string) string {
	return publicURLPattern.FindString(strings.ToLower(output))
}

func stopProcess(command *exec.Cmd) {
	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
}

func waitForContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
