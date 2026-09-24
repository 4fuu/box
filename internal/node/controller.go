// Package node is the deploy-node controller.
// It stores the node token in node.json and does not log it or container env values.
package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/4fuu/box/internal/api"
	"github.com/4fuu/box/internal/frp"
	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/paths"
	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/runtime"
)

const heartbeatEvery = 15 * time.Second

// File is the paired-node record.
type File struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Token    string `json:"token"`
	FRPToken string `json:"frp_token"`
	Server   string `json:"server"`
}

type comp struct {
	Name        string  `json:"name"`
	Volume      string  `json:"volume"`
	ImageRef    string  `json:"image_ref"`
	SSHPort     int     `json:"ssh_port"`
	BridgeIP    string  `json:"bridge_ip"`
	HostKey     string  `json:"host_key"`
	SSHDir      string  `json:"ssh_dir"`
	GuestSocket string  `json:"guest_socket"`
	CPU         float64 `json:"cpu"`
	Memory      int64   `json:"memory"`
	Disk        int64   `json:"disk"`
}

// Controller runs on a deploy node.
type Controller struct {
	DataDir  string
	API      string
	Runtime  runtime.Runtime
	Capacity func() (cpu int, memory, disk int64, err error)
	Client   *http.Client
	OnFRP    func(config string)

	mu       sync.Mutex
	file     File
	comps    map[string]*comp
	images   map[string]string
	domain   string
	keys     []string
	portals  []api.PortalReport
	rpc      net.Listener
	guests   map[string]net.Listener
	frpc     *exec.Cmd
	frpcText string
}

// Join exchanges a one-time code for a node token and writes node.json.
func Join(ctx context.Context, apiBase, frpServer, code, name, dataDir string) error {
	var resp api.JoinResponse
	if err := postJSON(ctx, http.DefaultClient, strings.TrimRight(apiBase, "/")+"/join", "", api.JoinRequest{Name: name, Code: code}, &resp); err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(File{ID: resp.ID, Name: name, Token: resp.Token, FRPToken: resp.FRPToken, Server: frpServer})
	if err != nil {
		return err
	}
	path := filepath.Join(dataDir, "node.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return nil
}

// Open loads a paired node. It does not contact the server yet.
func Open(dataDir, apiBase string) (*Controller, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "node.json"))
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.Token == "" || f.ID == "" {
		return nil, errors.New("node is not paired")
	}
	c := &Controller{
		DataDir:  dataDir,
		API:      strings.TrimRight(apiBase, "/"),
		Client:   http.DefaultClient,
		file:     f,
		comps:    map[string]*comp{},
		images:   map[string]string{},
		guests:   map[string]net.Listener{},
		Capacity: func() (int, int64, int64, error) { return HostCapacity(dataDir) },
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Controller) load() error {
	if raw, err := os.ReadFile(c.imagePath()); err == nil {
		if err := json.Unmarshal(raw, &c.images); err != nil {
			return err
		}
	}
	dir := filepath.Join(c.DataDir, "computers")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		var comp comp
		if err := json.Unmarshal(raw, &comp); err != nil {
			return err
		}
		c.comps[comp.Name] = &comp
	}
	return nil
}

// Serve accepts RPC, refreshes frpc, and heartbeats until ctx is done.
func (c *Controller) Serve(ctx context.Context) error {
	if c.Runtime == nil {
		return errors.New("podman is not available")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.rpc = ln
	c.mu.Unlock()
	if err := c.restoreGuests(); err != nil {
		ln.Close()
		return err
	}
	if err := c.reloadFRP(); err != nil {
		ln.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.stopFRPC()
	go c.loopHeartbeat(ctx)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go func() {
			defer conn.Close()
			_ = rpc.Serve(conn, c.handle)
		}()
	}
}

// RPCAddr is the loopback RPC listener. Empty before Serve.
func (c *Controller) RPCAddr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rpc == nil {
		return ""
	}
	return c.rpc.Addr().String()
}

func (c *Controller) loopHeartbeat(ctx context.Context) {
	_ = c.Heartbeat(ctx)
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = c.Heartbeat(ctx)
		}
	}
}

// Heartbeat reports capacity and applies the server's keys and portal list.
func (c *Controller) Heartbeat(ctx context.Context) error {
	cpu, mem, disk, err := c.capacity()
	if err != nil {
		return err
	}
	c.mu.Lock()
	usedCPU, usedMem, usedDisk := c.usedLocked()
	var images []string
	for name := range c.images {
		images = append(images, name)
	}
	var comps []api.ComputerReport
	for _, comp := range c.comps {
		state := "stopped"
		if comp.SSHPort > 0 {
			state = "running"
		}
		comps = append(comps, api.ComputerReport{Name: comp.Name, State: state})
	}
	c.mu.Unlock()
	var resp api.HeartbeatResponse
	err = c.post(ctx, "/heartbeat", api.HeartbeatRequest{
		CPU: cpu, Memory: mem, Disk: disk,
		UsedCPU: usedCPU, UsedMemory: usedMem, UsedDisk: usedDisk,
		Images: images, Computers: comps,
	}, &resp)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if resp.Domain != "" {
		c.domain = resp.Domain
	}
	c.keys = resp.Keys
	c.portals = resp.Portals
	compsCopy := make([]*comp, 0, len(c.comps))
	for _, comp := range c.comps {
		compsCopy = append(compsCopy, comp)
	}
	keys := append([]string(nil), c.keys...)
	c.mu.Unlock()
	for _, comp := range compsCopy {
		_ = writeKeys(comp.SSHDir, keys)
	}
	return c.reloadFRP()
}

func (c *Controller) handle(op string, body json.RawMessage) (any, error) {
	switch op {
	case rpc.OpCreate:
		var req rpc.CreateBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, c.create(req)
	case rpc.OpDelete:
		var req rpc.NameBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, c.delete(req.Name)
	case rpc.OpRestart:
		var req rpc.CreateBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, c.restart(req)
	case rpc.OpRename:
		var req rpc.RenameBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, c.rename(req.Old, req.New)
	case rpc.OpResize:
		var req rpc.ResizeBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, c.resize(req)
	case rpc.OpPull:
		var req rpc.PullBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, c.pull(req)
	case rpc.OpSyncKeys:
		var req rpc.KeysBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return nil, c.syncKeys(req.AuthorizedKeys)
	case rpc.OpHostKey:
		var req rpc.NameBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		line, err := c.hostKey(req.Name)
		if err != nil {
			return nil, err
		}
		return rpc.HostKeyBody{Public: line}, nil
	case rpc.OpStat:
		var req rpc.NameBody
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		return c.stat(req.Name)
	default:
		return nil, errors.New("unknown op")
	}
}

func (c *Controller) create(req rpc.CreateBody) error {
	if strings.TrimSpace(req.ImageRef) == "" || !c.haveRef(req.ImageRef) {
		return errors.New("image is not in the local cache")
	}
	if err := c.ensureCapacity(req.CPU, req.Memory, req.Disk); err != nil {
		return err
	}
	comp := &comp{
		Name: req.Name, Volume: req.Name, ImageRef: req.ImageRef,
		CPU: req.CPU, Memory: req.Memory, Disk: req.Disk,
		HostKey:     filepath.Join(c.DataDir, "computers", req.Name, "ssh_host_ed25519_key"),
		SSHDir:      filepath.Join(c.DataDir, "computers", req.Name, "ssh"),
		GuestSocket: filepath.Join(c.DataDir, "guests", req.Name+".sock"),
	}
	if err := c.Runtime.VolumeCreate(context.Background(), comp.Volume); err != nil {
		return err
	}
	if err := c.Runtime.PrepareHome(context.Background(), comp.Volume, comp.ImageRef); err != nil {
		return err
	}
	if _, err := keys.Generate(comp.HostKey, req.Name); err != nil {
		return err
	}
	c.mu.Lock()
	auth := append([]string(nil), c.keys...)
	c.mu.Unlock()
	if len(req.AuthorizedKeys) > 0 {
		auth = req.AuthorizedKeys
	}
	if err := writeKeys(comp.SSHDir, auth); err != nil {
		return err
	}
	if err := c.listenGuest(comp); err != nil {
		return err
	}
	ins, err := c.Runtime.Run(context.Background(), c.spec(comp, req.Env))
	if err != nil {
		return err
	}
	comp.BridgeIP = ins.BridgeIP
	comp.SSHPort = ins.SSHPort
	c.mu.Lock()
	c.comps[comp.Name] = comp
	c.mu.Unlock()
	if err := c.saveComp(comp); err != nil {
		return err
	}
	return c.reloadFRP()
}

func (c *Controller) spec(comp *comp, env map[string]string) runtime.Spec {
	return runtime.Spec{
		Name: comp.Name, ImageRef: comp.ImageRef, CPU: comp.CPU, Memory: comp.Memory,
		Volume: comp.Volume, Env: env, HostKey: comp.HostKey, SSHDir: comp.SSHDir,
		GuestSocket: comp.GuestSocket,
	}
}

func (c *Controller) delete(name string) error {
	comp := c.get(name)
	if comp == nil {
		return errors.New("not found")
	}
	_ = c.Runtime.RemoveContainer(context.Background(), name)
	_ = c.Runtime.VolumeRemove(context.Background(), comp.Volume)
	c.closeGuest(name)
	_ = os.RemoveAll(filepath.Join(c.DataDir, "computers", name))
	_ = os.Remove(comp.GuestSocket)
	c.mu.Lock()
	delete(c.comps, name)
	c.mu.Unlock()
	_ = os.Remove(c.compPath(name))
	return c.reloadFRP()
}

func (c *Controller) restart(req rpc.CreateBody) error {
	comp := c.get(req.Name)
	if comp == nil {
		return errors.New("not found")
	}
	// podman restart cannot change --env. Recreate the container and keep the volume.
	if err := c.Runtime.RemoveContainer(context.Background(), comp.Name); err != nil {
		return err
	}
	if len(req.AuthorizedKeys) > 0 {
		if err := writeKeys(comp.SSHDir, req.AuthorizedKeys); err != nil {
			return err
		}
	}
	ins, err := c.Runtime.Run(context.Background(), c.spec(comp, req.Env))
	if err != nil {
		return err
	}
	c.mu.Lock()
	comp.BridgeIP = ins.BridgeIP
	comp.SSHPort = ins.SSHPort
	c.mu.Unlock()
	if err := c.saveComp(comp); err != nil {
		return err
	}
	return c.reloadFRP()
}

func (c *Controller) rename(oldName, newName string) error {
	comp := c.get(oldName)
	if comp == nil {
		return errors.New("not found")
	}
	if err := c.Runtime.RenameContainer(context.Background(), oldName, newName); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.comps, oldName)
	comp.Name = newName
	c.comps[newName] = comp
	c.mu.Unlock()
	_ = os.Remove(c.compPath(oldName))
	return c.saveComp(comp)
}

func (c *Controller) resize(req rpc.ResizeBody) error {
	comp := c.get(req.Name)
	if comp == nil {
		return errors.New("not found")
	}
	if req.Disk != nil && *req.Disk < comp.Disk {
		return errors.New("disk only grows")
	}
	cpu, mem, disk := comp.CPU, comp.Memory, comp.Disk
	if req.CPU != nil {
		cpu = *req.CPU
	}
	if req.Memory != nil {
		mem = *req.Memory
	}
	if req.Disk != nil {
		disk = *req.Disk
	}
	if err := c.ensureCapacity(cpu-comp.CPU, mem-comp.Memory, disk-comp.Disk); err != nil {
		return err
	}
	if req.CPU != nil || req.Memory != nil {
		if err := c.Runtime.UpdateResources(context.Background(), comp.Name, req.CPU, req.Memory); err != nil {
			return err
		}
	}
	if req.Disk != nil && *req.Disk > comp.Disk {
		next := comp.Volume + "-next"
		if err := c.Runtime.VolumeCreate(context.Background(), next); err != nil {
			return err
		}
		if err := c.Runtime.CopyVolume(context.Background(), comp.Volume, next, comp.ImageRef); err != nil {
			_ = c.Runtime.VolumeRemove(context.Background(), next)
			return err
		}
		old := comp.Volume
		if err := c.Runtime.RemoveContainer(context.Background(), comp.Name); err != nil {
			return err
		}
		comp.Volume = next
		ins, err := c.Runtime.Run(context.Background(), c.spec(comp, nil))
		if err != nil {
			comp.Volume = old
			return err
		}
		comp.BridgeIP = ins.BridgeIP
		comp.SSHPort = ins.SSHPort
		_ = c.Runtime.VolumeRemove(context.Background(), old)
	}
	c.mu.Lock()
	comp.CPU, comp.Memory, comp.Disk = cpu, mem, disk
	c.mu.Unlock()
	return c.saveComp(comp)
}

func (c *Controller) pull(req rpc.PullBody) error {
	if err := c.Runtime.Pull(context.Background(), req.Ref); err != nil {
		return err
	}
	c.mu.Lock()
	if c.images == nil {
		c.images = map[string]string{}
	}
	c.images[req.Name] = req.Ref
	c.mu.Unlock()
	return c.saveImages()
}

func (c *Controller) syncKeys(lines []string) error {
	c.mu.Lock()
	c.keys = append([]string(nil), lines...)
	var comps []*comp
	for _, comp := range c.comps {
		comps = append(comps, comp)
	}
	c.mu.Unlock()
	for _, comp := range comps {
		if err := writeKeys(comp.SSHDir, lines); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) hostKey(name string) (string, error) {
	comp := c.get(name)
	if comp == nil {
		return "", errors.New("not found")
	}
	return keys.PublicLine(comp.HostKey)
}

func (c *Controller) stat(name string) (rpc.StatBody, error) {
	comp := c.get(name)
	if comp == nil {
		return rpc.StatBody{}, errors.New("not found")
	}
	body := rpc.StatBody{CPU: comp.CPU, Memory: comp.Memory, Disk: comp.Disk, Running: comp.SSHPort > 0}
	if c.Runtime != nil {
		rx, tx, ok := c.Runtime.Stats(context.Background(), name)
		body.RX, body.TX, body.HasNet = rx, tx, ok
	}
	return body, nil
}

func (c *Controller) haveRef(ref string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, got := range c.images {
		if got == ref {
			return true
		}
	}
	return false
}

func (c *Controller) ensureCapacity(cpu float64, memory, disk int64) error {
	totalCPU, totalMem, totalDisk, err := c.capacity()
	if err != nil {
		return err
	}
	c.mu.Lock()
	usedCPU, usedMem, usedDisk := c.usedLocked()
	c.mu.Unlock()
	if cpu > 0 && totalCPU > 0 && usedCPU+cpu > float64(totalCPU) {
		return errors.New(rpc.ErrNoCapacity)
	}
	if memory > 0 && totalMem > 0 && usedMem+memory > totalMem {
		return errors.New(rpc.ErrNoCapacity)
	}
	if disk > 0 && totalDisk > 0 && usedDisk+disk > totalDisk {
		return errors.New(rpc.ErrNoCapacity)
	}
	return nil
}

func (c *Controller) capacity() (int, int64, int64, error) {
	if c.Capacity == nil {
		return 0, 0, 0, nil
	}
	return c.Capacity()
}

func (c *Controller) usedLocked() (float64, int64, int64) {
	var cpu float64
	var mem, disk int64
	for _, comp := range c.comps {
		cpu += comp.CPU
		mem += comp.Memory
		disk += comp.Disk
	}
	return cpu, mem, disk
}

func (c *Controller) get(name string) *comp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.comps[name]
}

func (c *Controller) saveComp(comp *comp) error {
	if err := os.MkdirAll(filepath.Join(c.DataDir, "computers"), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(comp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.compPath(comp.Name), append(raw, '\n'), 0o600)
}

func (c *Controller) saveImages() error {
	c.mu.Lock()
	raw, err := json.MarshalIndent(c.images, "", "  ")
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(c.imagePath(), append(raw, '\n'), 0o600)
}

func (c *Controller) compPath(name string) string {
	return filepath.Join(c.DataDir, "computers", name+".json")
}

func (c *Controller) imagePath() string { return filepath.Join(c.DataDir, "images.json") }

func writeKeys(dir string, lines []string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(b.String()), 0o644)
}

func (c *Controller) reloadFRP() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rpc == nil {
		return nil
	}
	_, portStr, err := net.SplitHostPort(c.rpc.Addr().String())
	if err != nil {
		return err
	}
	rpcPort, _ := strconv.Atoi(portStr)
	host, sport, err := net.SplitHostPort(c.file.Server)
	if err != nil {
		host = c.file.Server
		sport = strconv.Itoa(paths.FRPPort)
	}
	serverPort, _ := strconv.Atoi(sport)
	proxies := []frp.Proxy{{
		Name: frp.RPCName(c.file.ID), Type: "stcp", LocalIP: "127.0.0.1", LocalPort: rpcPort, Secret: c.file.Token,
	}}
	for _, comp := range c.comps {
		if comp.SSHPort > 0 {
			proxies = append(proxies, frp.Proxy{
				Name: frp.SSHName(c.file.ID, comp.Name), Type: "stcp",
				LocalIP: "127.0.0.1", LocalPort: comp.SSHPort, Secret: c.file.Token,
			})
		}
	}
	for _, p := range c.portals {
		comp := c.comps[p.Container]
		if comp == nil || comp.BridgeIP == "" {
			continue
		}
		proxies = append(proxies, frp.Proxy{
			Name: frp.HTTPName(c.file.ID, p.Label), Type: "http",
			LocalIP: comp.BridgeIP, LocalPort: p.Port, CustomDomain: p.Host,
		})
	}
	text := frp.ClientTOML(host, serverPort, c.file.FRPToken, proxies)
	path := filepath.Join(c.DataDir, "frpc.toml")
	if text == c.frpcText {
		return nil
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	c.frpcText = text
	if c.OnFRP != nil {
		c.OnFRP(text)
	}
	c.swapFRPC(path)
	return nil
}

// swapFRPC restarts frpc when the proxy list changes. The caller holds c.mu.
// frpc stdout is discarded so a token in its log is not copied here.
func (c *Controller) swapFRPC(path string) {
	if c.frpc != nil && c.frpc.Process != nil {
		_ = c.frpc.Process.Kill()
		_, _ = c.frpc.Process.Wait()
		c.frpc = nil
	}
	bin, err := exec.LookPath("frpc")
	if err != nil {
		return
	}
	cmd := exec.Command(bin, "-c", path)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return
	}
	c.frpc = cmd
}

func (c *Controller) stopFRPC() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frpc != nil && c.frpc.Process != nil {
		_ = c.frpc.Process.Kill()
		_, _ = c.frpc.Process.Wait()
		c.frpc = nil
	}
}

func (c *Controller) post(ctx context.Context, path string, in, out any) error {
	return postJSON(ctx, c.client(), c.API+path, c.file.Token, in, out)
}

func (c *Controller) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

func postJSON(ctx context.Context, client *http.Client, url, token string, in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}

// Domain is the parent domain last reported by the server.
func (c *Controller) Domain() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.domain
}
