package fault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const ImageName = "crashlab-netem:local"

// Request is one fault to apply to a container.
type Request struct {
	Kind            string
	Delay           time.Duration
	Percent         float64
	AllowPrivileged bool
}

// Options controls fault cleanup.
type Options struct {
	AllowPrivileged bool
}

// Prepare builds the local netem helper image when it is missing.
func Prepare(ctx context.Context, dir string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker CLI is required to build %s", ImageName)
	}
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", ImageName, dir)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build %s: %w\n%s", ImageName, err, tail(buf.String()))
	}
	return nil
}

// Apply injects latency, packet loss, container termination, or CPU stress.
func Apply(ctx context.Context, cli *client.Client, containerID string, req Request) error {
	switch req.Kind {
	case "", "latency":
		return injectLatency(ctx, cli, containerID, req.Delay.String(), req.AllowPrivileged)
	case "loss":
		return injectLoss(ctx, cli, containerID, req.Percent, req.AllowPrivileged)
	case "kill":
		return cli.ContainerKill(ctx, containerID, "SIGKILL")
	case "cpu":
		return stressCPU(ctx, cli, containerID, req.Percent)
	default:
		return fmt.Errorf("unknown fault %q", req.Kind)
	}
}

// Inject applies egress netem delay inside the target container's network namespace.
// The qdisc remains after the helper exits. Delay is a tc duration such as 800ms.
// Kernels built without sch_netem get a same-namespace delay proxy instead.
func Inject(ctx context.Context, cli *client.Client, containerID, delay string) error {
	return injectLatency(ctx, cli, containerID, delay, false)
}

func injectLatency(ctx context.Context, cli *client.Client, containerID, delay string, allowPrivileged bool) error {
	out, err := run(ctx, cli, containerID, allowPrivileged, "inject", delay)
	if err == nil {
		return nil
	}
	if !strings.Contains(strings.ToLower(out+" "+err.Error()), "unknown") {
		return fmt.Errorf("inject latency on %s: %w\n%s", short(containerID), err, out)
	}
	port := servicePort(ctx, cli, containerID)
	log.Printf("kernel has no sch_netem; delaying %s port %d with a network-namespace proxy", short(containerID), port)
	if err := startFallback(ctx, cli, containerID, delay, port, allowPrivileged); err != nil {
		return fmt.Errorf("inject latency on %s: %w", short(containerID), err)
	}
	return nil
}

type clearActions struct {
	running  func() (bool, error)
	netem    func() error
	fallback func() error
	cpu      func() error
	start    func() error
}

// Clear removes network faults, stops CPU stress, and starts a container that was terminated.
// Every step runs even when an earlier step fails.
func Clear(ctx context.Context, cli *client.Client, containerID string) error {
	return ClearOptions(ctx, cli, containerID, Options{})
}

// ClearOptions removes a fault. A privileged helper retry runs only when AllowPrivileged is set.
func ClearOptions(ctx context.Context, cli *client.Client, containerID string, opts Options) error {
	return performClear(clearActions{
		running: func() (bool, error) { return containerRunning(ctx, cli, containerID) },
		netem: func() error {
			_, err := run(ctx, cli, containerID, opts.AllowPrivileged, "clear")
			return err
		},
		fallback: func() error { return stopFallback(ctx, cli, containerID) },
		cpu:      func() error { return stopCPU(ctx, cli, containerID) },
		start:    func() error { return ensureRunning(ctx, cli, containerID) },
	})
}

func performClear(actions clearActions) error {
	var running bool
	var runningErr error
	if actions.running != nil {
		running, runningErr = actions.running()
	}
	var netemErr, fallbackErr, cpuErr, startErr error
	if running && actions.netem != nil {
		netemErr = actions.netem()
	}
	if actions.fallback != nil {
		fallbackErr = actions.fallback()
	}
	if actions.cpu != nil {
		cpuErr = actions.cpu()
	}
	if actions.start != nil {
		startErr = actions.start()
	}
	return joinClearErrors(runningErr, netemErr, fallbackErr, cpuErr, startErr)
}

func joinClearErrors(errs ...error) error {
	var out []error
	for _, err := range errs {
		if err != nil {
			out = append(out, err)
		}
	}
	return errors.Join(out...)
}

func containerRunning(ctx context.Context, cli *client.Client, containerID string) (bool, error) {
	inspected, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, err
	}
	return inspected.State != nil && inspected.State.Running, nil
}

func injectLoss(ctx context.Context, cli *client.Client, containerID string, percent float64, allowPrivileged bool) error {
	out, err := run(ctx, cli, containerID, allowPrivileged, "inject-loss", fmt.Sprintf("%g", percent))
	if err == nil {
		return nil
	}
	if !strings.Contains(strings.ToLower(out+" "+err.Error()), "unknown") {
		return fmt.Errorf("inject loss on %s: %w\n%s", short(containerID), err, out)
	}
	port := servicePort(ctx, cli, containerID)
	log.Printf("kernel has no sch_netem; dropping %.0f%% of connections to %s port %d", percent, short(containerID), port)
	return startFallbackMode(ctx, cli, containerID, "fallback-loss", fmt.Sprintf("%g", percent), port, allowPrivileged)
}

func ensureRunning(ctx context.Context, cli *client.Client, containerID string) error {
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		inspected, err := cli.ContainerInspect(ctx, containerID)
		if err != nil {
			return err
		}
		if inspected.State != nil && inspected.State.Running {
			return nil
		}
		if inspected.State == nil || inspected.State.Status != "restarting" {
			if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
				last = err
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("container %s did not return to running", short(containerID))
	}
	return last
}

// Show returns the target interface qdisc, used to confirm the fault is gone.
func Show(ctx context.Context, cli *client.Client, containerID string) (string, error) {
	out, err := run(ctx, cli, containerID, false, "show")
	if err != nil {
		return out, fmt.Errorf("show qdisc on %s: %w\n%s", short(containerID), err, out)
	}
	return out, nil
}

func servicePort(ctx context.Context, cli *client.Client, containerID string) int {
	inspected, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return 8080
	}
	parse := func(raw string) int {
		num := strings.Split(raw, "/")[0]
		port, err := strconv.Atoi(num)
		if err != nil {
			return 0
		}
		return port
	}
	if inspected.NetworkSettings != nil {
		for port := range inspected.NetworkSettings.Ports {
			if parse(string(port)) == 8080 {
				return 8080
			}
		}
		for port := range inspected.NetworkSettings.Ports {
			if n := parse(string(port)); n > 0 {
				return n
			}
		}
	}
	if inspected.Config != nil {
		for port := range inspected.Config.ExposedPorts {
			if parse(string(port)) == 8080 {
				return 8080
			}
		}
	}
	return 8080
}

func startFallback(ctx context.Context, cli *client.Client, containerID, delay string, port int, allowPrivileged bool) error {
	return startFallbackMode(ctx, cli, containerID, "fallback", delay, port, allowPrivileged)
}

func startFallbackMode(ctx context.Context, cli *client.Client, containerID, mode, arg string, port int, allowPrivileged bool) error {
	if err := stopFallback(ctx, cli, containerID); err != nil {
		return err
	}
	out, err := startFallbackOnce(ctx, cli, containerID, mode, arg, port, false)
	if err == nil {
		return nil
	}
	if !allowPrivilegedRetry(allowPrivileged, out+" "+err.Error()) {
		return err
	}
	log.Printf("retrying delay proxy privileged because it was requested")
	_, err = startFallbackOnce(ctx, cli, containerID, mode, arg, port, true)
	return err
}

func startFallbackOnce(ctx context.Context, cli *client.Client, containerID, mode, arg string, port int, privileged bool) (string, error) {
	created, err := cli.ContainerCreate(ctx, &container.Config{
		Image: ImageName,
		Cmd:   []string{mode, arg, strconv.Itoa(port)},
		Tty:   true,
		Labels: map[string]string{
			"crashlab.role":   "delay",
			"crashlab.target": containerID,
		},
	}, &container.HostConfig{
		NetworkMode: container.NetworkMode("container:" + containerID),
		CapAdd:      []string{"NET_ADMIN"},
		Privileged:  privileged,
	}, nil, nil, "")
	if err != nil {
		return "", err
	}
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		removeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = cli.ContainerRemove(removeCtx, created.ID, container.RemoveOptions{Force: true})
		return "", err
	}
	deadline := time.Now().Add(10 * time.Second)
	var logs string
	for time.Now().Before(deadline) {
		inspected, err := cli.ContainerInspect(ctx, created.ID)
		if err != nil {
			return logs, err
		}
		logs, _ = readLogs(ctx, cli, created.ID)
		if inspected.State != nil && inspected.State.Running && strings.Contains(logs, "listening") {
			return logs, nil
		}
		if inspected.State != nil && !inspected.State.Running && inspected.State.Status == "exited" {
			_ = cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
			return logs, fmt.Errorf("delay proxy exited %d\n%s", inspected.State.ExitCode, logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return logs, fmt.Errorf("delay proxy did not start\n%s", logs)
}

func stopFallback(ctx context.Context, cli *client.Client, containerID string) error {
	listed, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "crashlab.target="+containerID)),
	})
	if err != nil {
		return err
	}
	var first error
	for _, ctr := range listed {
		if err := cli.ContainerRemove(ctx, ctr.ID, container.RemoveOptions{Force: true}); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func run(ctx context.Context, cli *client.Client, containerID string, allowPrivileged bool, args ...string) (string, error) {
	out, err := runOnce(ctx, cli, containerID, false, args...)
	if err == nil {
		return out, nil
	}
	if !allowPrivilegedRetry(allowPrivileged, out+" "+err.Error()) {
		return out, err
	}
	log.Printf("retrying netem helper privileged because it was requested")
	return runOnce(ctx, cli, containerID, true, args...)
}

func allowPrivilegedRetry(enabled bool, msg string) bool {
	return enabled && needsPrivilege(msg)
}

func runOnce(ctx context.Context, cli *client.Client, containerID string, privileged bool, args ...string) (string, error) {
	created, err := cli.ContainerCreate(ctx, &container.Config{
		Image: ImageName,
		Cmd:   args,
		Tty:   true,
	}, &container.HostConfig{
		NetworkMode: container.NetworkMode("container:" + containerID),
		CapAdd:      []string{"NET_ADMIN"},
		Privileged:  privileged,
	}, nil, nil, "")
	if err != nil {
		return "", err
	}
	defer func() {
		removeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = cli.ContainerRemove(removeCtx, created.ID, container.RemoveOptions{Force: true})
	}()
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", err
	}
	statusCh, errCh := cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	var code int64
	select {
	case err := <-errCh:
		if err != nil {
			return "", err
		}
	case status := <-statusCh:
		code = status.StatusCode
	case <-ctx.Done():
		return "", ctx.Err()
	}
	logs, logErr := readLogs(ctx, cli, created.ID)
	if code != 0 {
		if logErr != nil {
			return logs, fmt.Errorf("helper exited %d (%v)", code, logErr)
		}
		return logs, fmt.Errorf("helper exited %d", code)
	}
	return logs, nil
}

func readLogs(ctx context.Context, cli *client.Client, id string) (string, error) {
	reader, err := cli.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return "", err
	}
	defer reader.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(reader)
	return buf.String(), nil
}

func needsPrivilege(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "net_admin")
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func stressCPU(ctx context.Context, cli *client.Client, containerID string, percent float64) error {
	if err := stopCPU(ctx, cli, containerID); err != nil {
		return err
	}
	bin, err := exportDelayBinary(ctx)
	if err != nil {
		return err
	}
	copy := exec.CommandContext(ctx, "docker", "cp", bin, containerID+":/tmp/crashlab-cpu")
	if out, err := copy.CombinedOutput(); err != nil {
		return fmt.Errorf("copy cpu helper: %w\n%s", err, out)
	}
	created, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"/tmp/crashlab-cpu", "cpu", "--percent", fmt.Sprintf("%g", percent)},
		Detach:       true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("start cpu stress: %w", err)
	}
	if err := cli.ContainerExecStart(ctx, created.ID, container.ExecStartOptions{Detach: true}); err != nil {
		return fmt.Errorf("start cpu stress: %w", err)
	}
	return nil
}

func stopCPU(ctx context.Context, cli *client.Client, containerID string) error {
	created, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"/bin/sh", "-c", "if [ -f /tmp/crashlab-cpu.pid ]; then kill $(cat /tmp/crashlab-cpu.pid) 2>/dev/null || true; rm -f /tmp/crashlab-cpu.pid; fi"},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil
	}
	_ = cli.ContainerExecStart(ctx, created.ID, container.ExecStartOptions{})
	return nil
}

func exportDelayBinary(ctx context.Context) (string, error) {
	create := exec.CommandContext(ctx, "docker", "create", ImageName)
	out, err := create.Output()
	if err != nil {
		return "", fmt.Errorf("create helper: %w", err)
	}
	id := strings.TrimSpace(string(out))
	defer exec.Command("docker", "rm", id).Run()
	dest := filepath.Join(os.TempDir(), "crashlab-cpu-bin")
	cp := exec.CommandContext(ctx, "docker", "cp", id+":/usr/local/bin/delayd", dest)
	if output, err := cp.CombinedOutput(); err != nil {
		return "", fmt.Errorf("export helper: %w\n%s", err, output)
	}
	return dest, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return s[len(s)-2000:]
	}
	return s
}
