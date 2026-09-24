package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/4fuu/box/internal/paths"
	"github.com/4fuu/box/internal/size"
)

// Cmd runs a binary and returns combined output.
type Cmd func(ctx context.Context, name string, args ...string) ([]byte, error)

// Podman shells out to podman. Failures do not include the argument list,
// because --env values are secrets.
type Podman struct {
	RunCmd Cmd
}

func (p *Podman) cmd(ctx context.Context, args ...string) ([]byte, error) {
	run := p.RunCmd
	if run == nil {
		run = defaultCmd
	}
	out, err := run(ctx, "podman", args...)
	if err != nil {
		return out, fmt.Errorf("podman failed")
	}
	return out, nil
}

func defaultCmd(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	return c.CombinedOutput()
}

func (p *Podman) PrepareHome(ctx context.Context, volume, imageRef string) error {
	script := "if [ ! -e /var/lib/box/home/.box-ready ]; then cp -a /home/box/. /var/lib/box/home/; touch /var/lib/box/home/.box-ready; fi"
	_, err := p.cmd(ctx, "run", "--rm", "--entrypoint", "/bin/bash",
		"--volume", volume+":/var/lib/box", imageRef, "-c", script)
	return err
}

func (p *Podman) VolumeCreate(ctx context.Context, name string) error {
	_, err := p.cmd(ctx, "volume", "create", name)
	return err
}

func (p *Podman) VolumeRemove(ctx context.Context, name string) error {
	_, err := p.cmd(ctx, "volume", "rm", "-f", name)
	return err
}

func (p *Podman) CopyVolume(ctx context.Context, from, to, imageRef string) error {
	_, err := p.cmd(ctx, "run", "--rm", "--entrypoint", "/bin/bash",
		"--volume", from+":/from:ro", "--volume", to+":/to",
		imageRef, "-c", "cp -a /from/. /to/")
	return err
}

func (p *Podman) Run(ctx context.Context, spec Spec) (Inspected, error) {
	if _, err := p.cmd(ctx, RunArgs(spec)...); err != nil {
		return Inspected{}, err
	}
	return p.Inspect(ctx, spec.Name)
}

func (p *Podman) RemoveContainer(ctx context.Context, name string) error {
	_, err := p.cmd(ctx, "rm", "-f", name)
	return err
}

func (p *Podman) RenameContainer(ctx context.Context, oldName, newName string) error {
	_, err := p.cmd(ctx, "rename", oldName, newName)
	return err
}

func (p *Podman) UpdateResources(ctx context.Context, name string, cpu *float64, memory *int64) error {
	args := []string{"update"}
	if cpu != nil {
		args = append(args, "--cpus", size.FormatCPU(*cpu))
	}
	if memory != nil {
		args = append(args, "--memory", size.FormatBytes(*memory))
	}
	args = append(args, name)
	_, err := p.cmd(ctx, args...)
	return err
}

func (p *Podman) Inspect(ctx context.Context, name string) (Inspected, error) {
	ip, err := p.cmd(ctx, "inspect", "--format", "{{.NetworkSettings.IPAddress}}", name)
	if err != nil {
		return Inspected{}, err
	}
	port, err := p.cmd(ctx, "port", name, strconv.Itoa(paths.SSHDPort)+"/tcp")
	if err != nil {
		return Inspected{}, err
	}
	running, err := p.cmd(ctx, "inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return Inspected{}, err
	}
	n, err := parsePublishedPort(string(port))
	if err != nil {
		return Inspected{}, err
	}
	return Inspected{
		BridgeIP: strings.TrimSpace(string(ip)),
		SSHPort:  n,
		Running:  strings.TrimSpace(string(running)) == "true",
	}, nil
}

func (p *Podman) Pull(ctx context.Context, ref string) error {
	_, err := p.cmd(ctx, "pull", ref)
	return err
}

func (p *Podman) ImageExists(ctx context.Context, ref string) (bool, error) {
	run := p.RunCmd
	if run == nil {
		run = defaultCmd
	}
	_, err := run(ctx, "podman", "image", "exists", ref)
	if err == nil {
		return true, nil
	}
	return false, nil
}

func (p *Podman) Stats(ctx context.Context, name string) (int64, int64, bool) {
	return 0, 0, false
}

// RunArgs is the podman run invocation for a computer.
// Port 2222 is published on loopback only. HTTP is not published.
func RunArgs(spec Spec) []string {
	args := []string{
		"run", "-d",
		"--name", spec.Name,
		"--systemd=always",
		"--restart=always",
		"--cpus", size.FormatCPU(spec.CPU),
		"--memory", size.FormatBytes(spec.Memory),
		"--volume", spec.Volume + ":" + paths.VolumeMount,
		"--mount", fmt.Sprintf("type=volume,source=%s,destination=/home/box,subpath=home", spec.Volume),
		"--publish", fmt.Sprintf("127.0.0.1::%d", paths.SSHDPort),
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
	if spec.SSHDir != "" {
		args = append(args, "--mount", fmt.Sprintf("type=bind,src=%s,dst=/home/box/.ssh,readonly", spec.SSHDir))
	}
	if spec.HostKey != "" {
		args = append(args,
			"--mount", fmt.Sprintf("type=bind,src=%s,dst=/etc/ssh/ssh_host_ed25519_key,readonly", spec.HostKey),
			"--mount", fmt.Sprintf("type=bind,src=%s.pub,dst=/etc/ssh/ssh_host_ed25519_key.pub,readonly", spec.HostKey),
		)
	}
	if spec.GuestSocket != "" {
		args = append(args, "--mount", fmt.Sprintf("type=bind,src=%s,dst=%s", spec.GuestSocket, paths.GuestSocket))
	}
	args = append(args, spec.ImageRef)
	return args
}

func parsePublishedPort(out string) (int, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return 0, fmt.Errorf("ssh port is not published")
	}
	// podman port prints one or more "127.0.0.1:PORT" lines.
	line := strings.Split(out, "\n")[0]
	line = strings.TrimSpace(line)
	i := strings.LastIndex(line, ":")
	if i < 0 {
		return 0, fmt.Errorf("ssh port is not published")
	}
	n, err := strconv.Atoi(line[i+1:])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("ssh port is not published")
	}
	return n, nil
}
