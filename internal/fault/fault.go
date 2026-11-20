package fault

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const ImageName = "crashlab-netem:local"

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

// Inject applies egress netem delay inside the target container's network namespace.
// The qdisc remains after the helper exits. Delay is a tc duration such as 800ms.
// Kernels built without sch_netem get a same-namespace delay proxy instead.
func Inject(ctx context.Context, cli *client.Client, containerID, delay string) error {
	out, err := run(ctx, cli, containerID, "inject", delay)
	if err == nil {
		return nil
	}
	if !strings.Contains(strings.ToLower(out+" "+err.Error()), "unknown") {
		return fmt.Errorf("inject latency on %s: %w\n%s", short(containerID), err, out)
	}
	port := servicePort(ctx, cli, containerID)
	log.Printf("kernel has no sch_netem; delaying %s port %d with a network-namespace proxy", short(containerID), port)
	if err := startFallback(ctx, cli, containerID, delay, port); err != nil {
		return fmt.Errorf("inject latency on %s: %w", short(containerID), err)
	}
	return nil
}

// Clear removes the root qdisc and any delay proxy. Missing faults are not an error.
func Clear(ctx context.Context, cli *client.Client, containerID string) error {
	out, err := run(ctx, cli, containerID, "clear")
	stopErr := stopFallback(ctx, cli, containerID)
	if err != nil {
		return fmt.Errorf("clear latency on %s: %w\n%s", short(containerID), err, out)
	}
	return stopErr
}

// Show returns the target interface qdisc, used to confirm the fault is gone.
func Show(ctx context.Context, cli *client.Client, containerID string) (string, error) {
	out, err := run(ctx, cli, containerID, "show")
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

func startFallback(ctx context.Context, cli *client.Client, containerID, delay string, port int) error {
	if err := stopFallback(ctx, cli, containerID); err != nil {
		return err
	}
	out, err := startFallbackOnce(ctx, cli, containerID, delay, port, false)
	if err == nil {
		return nil
	}
	if !needsPrivilege(out) && !needsPrivilege(err.Error()) {
		return err
	}
	_, err = startFallbackOnce(ctx, cli, containerID, delay, port, true)
	return err
}

func startFallbackOnce(ctx context.Context, cli *client.Client, containerID, delay string, port int, privileged bool) (string, error) {
	created, err := cli.ContainerCreate(ctx, &container.Config{
		Image: ImageName,
		Cmd:   []string{"fallback", delay, strconv.Itoa(port)},
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

func run(ctx context.Context, cli *client.Client, containerID string, args ...string) (string, error) {
	out, err := runOnce(ctx, cli, containerID, false, args...)
	if err == nil {
		return out, nil
	}
	if !needsPrivilege(out) && !needsPrivilege(err.Error()) {
		return out, err
	}
	return runOnce(ctx, cli, containerID, true, args...)
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

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		return s[len(s)-2000:]
	}
	return s
}
