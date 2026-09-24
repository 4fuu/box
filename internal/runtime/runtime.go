// Package runtime is how the controller talks to Podman.
// Containers are unprivileged: no --privileged, no host network, no Docker socket.
package runtime

import "context"

// Spec is one computer. Env is passed to podman and is not persisted by this package.
type Spec struct {
	Name        string
	ImageRef    string
	CPU         float64
	Memory      int64
	Volume      string
	Env         map[string]string
	HostKey     string
	SSHDir      string
	GuestSocket string
}

// Inspected is what the controller needs after podman run.
type Inspected struct {
	BridgeIP string
	SSHPort  int
	Running  bool
}

// Runtime is the Podman surface the controller uses.
type Runtime interface {
	PrepareHome(ctx context.Context, volume, imageRef string) error
	VolumeCreate(ctx context.Context, name string) error
	VolumeRemove(ctx context.Context, name string) error
	CopyVolume(ctx context.Context, from, to, imageRef string) error
	Run(ctx context.Context, spec Spec) (Inspected, error)
	RemoveContainer(ctx context.Context, name string) error
	RenameContainer(ctx context.Context, oldName, newName string) error
	UpdateResources(ctx context.Context, name string, cpu *float64, memory *int64) error
	Inspect(ctx context.Context, name string) (Inspected, error)
	Pull(ctx context.Context, ref string) error
	ImageExists(ctx context.Context, ref string) (bool, error)
	Stats(ctx context.Context, name string) (rx, tx int64, ok bool)
}
