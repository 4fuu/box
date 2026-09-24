// Package control is the server-side behavior shared by the REPL and the localhost CLI.
package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/4fuu/box/internal/ident"
	"github.com/4fuu/box/internal/keys"
	"github.com/4fuu/box/internal/rpc"
	"github.com/4fuu/box/internal/secret"
	"github.com/4fuu/box/internal/size"
	"github.com/4fuu/box/internal/store"
	"golang.org/x/crypto/ssh"
)

const (
	PairingTTL = 10 * time.Minute
	OnlineFor  = 45 * time.Second
)

// ErrUnreachable marks a failure to dial a node, as opposed to an error the node returned.
var ErrUnreachable = errors.New("unreachable")

// NodeClient is how the server asks a paired node to do work.
type NodeClient interface {
	Create(ctx context.Context, nodeID string, body rpc.CreateBody) error
	Delete(ctx context.Context, nodeID, name string) error
	Restart(ctx context.Context, nodeID string, body rpc.CreateBody) error
	Rename(ctx context.Context, nodeID, oldName, newName string) error
	Resize(ctx context.Context, nodeID string, body rpc.ResizeBody) error
	Pull(ctx context.Context, nodeID string, body rpc.PullBody) error
	SyncKeys(ctx context.Context, nodeID string, keys []string) error
	HostKey(ctx context.Context, nodeID, name string) (string, error)
	Stat(ctx context.Context, nodeID, name string) (rpc.StatBody, error)
}

// Service is the control plane.
type Service struct {
	Store        *store.Store
	Nodes        NodeClient
	Domain       string
	GitHubPublic string
	PairingTTL   time.Duration
	OnlineFor    time.Duration
}

func (s *Service) ttl() time.Duration {
	if s.PairingTTL == 0 {
		return PairingTTL
	}
	return s.PairingTTL
}

func (s *Service) onlineFor() time.Duration {
	if s.OnlineFor == 0 {
		return OnlineFor
	}
	return s.OnlineFor
}

type Pairing struct {
	Secret  string
	Expires time.Time
}

func (s *Service) PairClient() (Pairing, error) {
	return s.pair(store.KindClient, func() (string, error) { return secret.ClientPassword() })
}

func (s *Service) PairNode() (Pairing, error) {
	return s.pair(store.KindNode, secret.NodeCode)
}

func (s *Service) pair(kind string, gen func() (string, error)) (Pairing, error) {
	raw, err := gen()
	if err != nil {
		return Pairing{}, err
	}
	exp, err := s.Store.NewPairing(kind, raw, s.ttl())
	if err != nil {
		return Pairing{}, err
	}
	return Pairing{Secret: raw, Expires: exp}, nil
}

// ConsumeClient checks a one-time password. The password is not retained.
func (s *Service) ConsumeClient(raw string) error {
	raw = secret.NormalizePassword(raw)
	err := s.Store.ConsumePairing(store.KindClient, raw)
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrExpired) {
		return errors.New("password expired")
	}
	return errors.New("invalid password")
}

// ConsumeNode checks a pairing code.
func (s *Service) ConsumeNode(raw string) error {
	err := s.Store.ConsumePairing(store.KindNode, strings.TrimSpace(raw))
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrExpired) {
		return errors.New("code expired")
	}
	return errors.New("invalid code")
}

// Bind stores a client public key. A second bind of the same key is a no-op.
func (s *Service) Bind(pub ssh.PublicKey, comment string) error {
	fp := keys.Fingerprint(pub)
	if _, err := s.Store.FindKeyByFingerprint(fp); err == nil {
		return nil
	}
	line := keys.Line(pub, comment)
	return s.Store.BindKey(line, comment, fp)
}

func (s *Service) HasKey(pub ssh.PublicKey) (bool, error) {
	fp := keys.Fingerprint(pub)
	_, err := s.Store.FindKeyByFingerprint(fp)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

type KeyView struct {
	Fingerprint string    `json:"fingerprint"`
	Comment     string    `json:"comment"`
	BoundAt     time.Time `json:"bound_at"`
}

func (s *Service) Keys() ([]KeyView, error) {
	list, err := s.Store.ListKeys()
	if err != nil {
		return nil, err
	}
	out := make([]KeyView, 0, len(list))
	for _, k := range list {
		out = append(out, KeyView{Fingerprint: k.Fingerprint, Comment: k.Comment, BoundAt: k.BoundAt})
	}
	return out, nil
}

func (s *Service) RemoveKey(match string) error {
	k, err := s.Store.FindKeyByFingerprint(strings.TrimSpace(match))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errors.New("unknown key")
		}
		return err
	}
	if err := s.Store.RemoveKey(k.ID); err != nil {
		return err
	}
	s.PushKeys(context.Background())
	return nil
}

// AuthorizedLines is every bound client key plus the server GitHub public key.
func (s *Service) AuthorizedLines() ([]string, error) {
	list, err := s.Store.ListKeys()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, k := range list {
		out = append(out, k.Public)
	}
	gh := strings.TrimSpace(s.GitHubPublic)
	if gh != "" {
		out = append(out, gh)
	}
	return out, nil
}

// PushKeys updates online nodes. Offline nodes catch up on their next heartbeat.
func (s *Service) PushKeys(ctx context.Context) {
	if s.Nodes == nil {
		return
	}
	lines, err := s.AuthorizedLines()
	if err != nil {
		return
	}
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return
	}
	for _, n := range nodes {
		if !s.Online(n) {
			continue
		}
		_ = s.Nodes.SyncKeys(ctx, n.ID, lines)
	}
}

func (s *Service) WhoAmI(pub ssh.PublicKey) (string, error) {
	if pub == nil {
		return "", errors.New("no key")
	}
	fp := keys.Fingerprint(pub)
	k, err := s.Store.FindKeyByFingerprint(fp)
	if err != nil {
		return fp, nil
	}
	if k.Comment == "" {
		return fp, nil
	}
	return fp + " " + k.Comment, nil
}

func (s *Service) KeyCopy() (string, error) {
	line := s.GitHubPublic
	if strings.TrimSpace(line) == "" {
		return "", errors.New("github key is not available")
	}
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	return line, nil
}

func (s *Service) SetEnv(name, value string) (string, error) {
	if err := ident.Env(name); err != nil {
		return "", err
	}
	if err := s.Store.SetEnv(name, value); err != nil {
		return "", err
	}
	return name + " set. running containers keep the old value until restart", nil
}

func (s *Service) EnvNames() ([]string, error) { return s.Store.EnvNames() }

func (s *Service) DeleteEnv(name string) error {
	if err := s.Store.DeleteEnv(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("env %s is not set", name)
		}
		return err
	}
	return nil
}

type Status struct {
	Domain    string `json:"domain"`
	Keys      int    `json:"keys"`
	Nodes     int    `json:"nodes"`
	Computers int    `json:"computers"`
}

func (s *Service) Status() (Status, error) {
	st := Status{Domain: s.Domain}
	keys, err := s.Store.ListKeys()
	if err != nil {
		return st, err
	}
	st.Keys = len(keys)
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return st, err
	}
	st.Nodes = len(nodes)
	comps, err := s.Store.ListComputers()
	if err != nil {
		return st, err
	}
	st.Computers = len(comps)
	return st, nil
}

type Defaults struct {
	Node   string  `json:"node"`
	Image  string  `json:"image"`
	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`
	Disk   int64   `json:"disk"`
}

func (s *Service) Defaults() (Defaults, error) {
	d := Defaults{CPU: size.DefaultCPU, Memory: size.DefaultMemory, Disk: size.DefaultDisk}
	if img, err := s.Store.DefaultImage(); err == nil {
		d.Image = img.Name
	}
	if name, ok, err := s.Store.Meta("default_node"); err != nil {
		return d, err
	} else if ok {
		d.Node = name
	}
	if d.Node == "" {
		if only, ok := s.onlyOnline(); ok {
			d.Node = only.Name
		}
	}
	return d, nil
}

func (s *Service) Online(n store.Node) bool {
	if n.LastHeartbeat.IsZero() {
		return false
	}
	return s.Store.Now().Sub(n.LastHeartbeat) <= s.onlineFor()
}

func (s *Service) onlyOnline() (store.Node, bool) {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		return store.Node{}, false
	}
	var hit store.Node
	n := 0
	for _, node := range nodes {
		if s.Online(node) {
			hit = node
			n++
		}
	}
	if n == 1 {
		return hit, true
	}
	return store.Node{}, false
}

func (s *Service) IsComputer(name string) bool {
	if name == "" || name == "box" || strings.HasPrefix(name, "pair+") {
		return false
	}
	_, err := s.Store.Computer(name)
	return err == nil
}

type ComputerView struct {
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Node    string   `json:"node"`
	Image   string   `json:"image"`
	Portals []string `json:"portals"`
}

func (s *Service) ListComputers() ([]ComputerView, error) {
	list, err := s.Store.ListComputers()
	if err != nil {
		return nil, err
	}
	out := make([]ComputerView, 0, len(list))
	for _, c := range list {
		ports, err := s.Store.PortalsByContainer(c.Name)
		if err != nil {
			return nil, err
		}
		labels := make([]string, 0, len(ports))
		for _, p := range ports {
			labels = append(labels, p.Label)
		}
		out = append(out, ComputerView{
			Name: c.Name, State: s.displayState(c), Node: c.NodeName, Image: c.Image, Portals: labels,
		})
	}
	return out, nil
}

func (s *Service) displayState(c store.Computer) string {
	if !s.Online(store.Node{LastHeartbeat: c.Heartbeat}) {
		return "offline"
	}
	if c.State == "" {
		return store.StateStopped
	}
	return c.State
}

type NodeView struct {
	Name       string            `json:"name"`
	Online     bool              `json:"online"`
	CPU        int               `json:"cpu"`
	Memory     int64             `json:"memory"`
	Disk       int64             `json:"disk"`
	UsedCPU    float64           `json:"used_cpu"`
	UsedMemory int64             `json:"used_memory"`
	UsedDisk   int64             `json:"used_disk"`
	Tags       map[string]string `json:"tags"`
	Images     []string          `json:"images"`
}

func (s *Service) ListNodes() ([]NodeView, error) {
	list, err := s.Store.ListNodes()
	if err != nil {
		return nil, err
	}
	out := make([]NodeView, 0, len(list))
	for _, n := range list {
		imgs := n.Images
		if imgs == nil {
			imgs = []string{}
		}
		tags := n.Tags
		if tags == nil {
			tags = map[string]string{}
		}
		out = append(out, NodeView{
			Name: n.Name, Online: s.Online(n), CPU: n.CPU, Memory: n.Memory, Disk: n.Disk,
			UsedCPU: n.UsedCPU, UsedMemory: n.UsedMemory, UsedDisk: n.UsedDisk,
			Tags: tags, Images: imgs,
		})
	}
	return out, nil
}

func (s *Service) TagNode(name, key, val string) error {
	if key == "" {
		return errors.New("invalid tag")
	}
	if _, err := s.Store.NodeByName(name); err != nil {
		return s.missingNode(err, name)
	}
	return s.Store.TagNode(name, key, val)
}

func (s *Service) RemoveNode(name string) error {
	n, err := s.Store.NodeByName(name)
	if err != nil {
		return s.missingNode(err, name)
	}
	if err := s.Store.DeleteNode(n.ID); err != nil {
		if errors.Is(err, store.ErrInUse) {
			return fmt.Errorf("node %s still has computers", name)
		}
		return err
	}
	return nil
}

type ImageView struct {
	Name    string   `json:"name"`
	Ref     string   `json:"ref"`
	Default bool     `json:"default"`
	Nodes   []string `json:"nodes"`
}

func (s *Service) ListImages() ([]ImageView, error) {
	list, err := s.Store.ListImages()
	if err != nil {
		return nil, err
	}
	out := make([]ImageView, 0, len(list))
	for _, img := range list {
		nodes, err := s.Store.NodesWithImage(img.Name)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(nodes))
		for _, n := range nodes {
			names = append(names, n.Name)
		}
		out = append(out, ImageView{Name: img.Name, Ref: img.Ref, Default: img.Default, Nodes: names})
	}
	return out, nil
}

func (s *Service) AddImage(name, ref string) error {
	if err := ident.Image(name); err != nil {
		return err
	}
	if strings.TrimSpace(ref) == "" {
		return errors.New("image ref is required")
	}
	if _, err := s.Store.Image(name); err == nil {
		return fmt.Errorf("image %s already exists", name)
	}
	return s.Store.AddImage(name, ref)
}

func (s *Service) RemoveImage(name string) error {
	if err := s.Store.DeleteImage(name); err != nil {
		if errors.Is(err, store.ErrInUse) {
			return fmt.Errorf("image %s is in use", name)
		}
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("image %s is not registered", name)
		}
		return err
	}
	return nil
}

func (s *Service) SetDefaultImage(name string) error {
	if err := s.Store.SetDefaultImage(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("image %s is not registered", name)
		}
		return err
	}
	return nil
}

func (s *Service) Pull(ctx context.Context, image, nodeName string) error {
	img, err := s.Store.Image(image)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("image %s is not registered", image)
		}
		return err
	}
	var nodes []store.Node
	if nodeName != "" {
		n, err := s.Store.NodeByName(nodeName)
		if err != nil {
			return s.missingNode(err, nodeName)
		}
		if !s.Online(n) {
			return fmt.Errorf("node %s is offline", nodeName)
		}
		nodes = []store.Node{n}
	} else {
		all, err := s.Store.ListNodes()
		if err != nil {
			return err
		}
		for _, n := range all {
			if s.Online(n) {
				nodes = append(nodes, n)
			}
		}
		if len(nodes) == 0 {
			return errors.New("no online node")
		}
	}
	if s.Nodes == nil {
		return errors.New("no online node")
	}
	for _, n := range nodes {
		if err := s.Nodes.Pull(ctx, n.ID, rpc.PullBody{Name: img.Name, Ref: img.Ref}); err != nil {
			return s.nodeErr(n.Name, err)
		}
		if err := s.Store.AddNodeImage(n.ID, img.Name); err != nil {
			return err
		}
	}
	return nil
}

type CreateInput struct {
	Name   string
	Image  string
	Node   string
	CPU    float64
	Memory int64
	Disk   int64
}

type Created struct {
	Name  string `json:"name"`
	Node  string `json:"node"`
	Image string `json:"image"`
	SSH   string `json:"ssh"`
	HTTP  string `json:"http"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Created, error) {
	if err := ident.Computer(in.Name); err != nil {
		return Created{}, err
	}
	if _, err := s.Store.Computer(in.Name); err == nil {
		return Created{}, fmt.Errorf("%s already exists", in.Name)
	}
	def, err := s.Defaults()
	if err != nil {
		return Created{}, err
	}
	if in.CPU == 0 {
		in.CPU = def.CPU
	}
	if in.Memory == 0 {
		in.Memory = def.Memory
	}
	if in.Disk == 0 {
		in.Disk = def.Disk
	}
	if in.Image == "" {
		in.Image = def.Image
	}
	if in.Image == "" {
		return Created{}, errors.New("no default image")
	}
	img, err := s.Store.Image(in.Image)
	if err != nil {
		return Created{}, fmt.Errorf("image %s is not registered", in.Image)
	}
	node, err := s.place(in.Node, img.Name, in.CPU, in.Memory, in.Disk)
	if err != nil {
		return Created{}, err
	}
	if err := s.Store.CreateComputer(store.Computer{
		Name: in.Name, NodeID: node.ID, Image: img.Name,
		CPU: in.CPU, Memory: in.Memory, Disk: in.Disk, State: store.StateRunning,
	}); err != nil {
		return Created{}, err
	}
	body, err := s.createBody(in.Name, img.Ref, in.CPU, in.Memory, in.Disk)
	if err != nil {
		_ = s.Store.DeleteComputer(in.Name)
		return Created{}, err
	}
	if s.Nodes == nil {
		_ = s.Store.DeleteComputer(in.Name)
		return Created{}, fmt.Errorf("node %s is unreachable", node.Name)
	}
	if err := s.Nodes.Create(ctx, node.ID, body); err != nil {
		_ = s.Store.DeleteComputer(in.Name)
		if err.Error() == rpc.ErrNoCapacity {
			return Created{}, s.capacityMessage(node, img.Name)
		}
		return Created{}, s.nodeErr(node.Name, err)
	}
	return Created{
		Name:  in.Name,
		Node:  node.Name,
		Image: img.Name,
		SSH:   fmt.Sprintf("ssh %s@%s", in.Name, s.Domain),
		HTTP:  "http://" + ident.Hostname(in.Name, s.Domain),
	}, nil
}

func (s *Service) createBody(name, ref string, cpu float64, memory, disk int64) (rpc.CreateBody, error) {
	env, err := s.Store.EnvAll()
	if err != nil {
		return rpc.CreateBody{}, err
	}
	lines, err := s.AuthorizedLines()
	if err != nil {
		return rpc.CreateBody{}, err
	}
	return rpc.CreateBody{
		Name: name, ImageRef: ref, CPU: cpu, Memory: memory, Disk: disk,
		Env: env, AuthorizedKeys: lines,
	}, nil
}

func (s *Service) place(nodeName, image string, cpu float64, memory, disk int64) (store.Node, error) {
	var node store.Node
	var err error
	if nodeName != "" {
		node, err = s.Store.NodeByName(nodeName)
		if err != nil {
			return store.Node{}, s.missingNode(err, nodeName)
		}
	} else {
		def, err := s.Defaults()
		if err != nil {
			return store.Node{}, err
		}
		if def.Node == "" {
			return store.Node{}, errors.New("node is required")
		}
		node, err = s.Store.NodeByName(def.Node)
		if err != nil {
			return store.Node{}, s.missingNode(err, def.Node)
		}
	}
	if !s.Online(node) {
		return store.Node{}, fmt.Errorf("node %s is offline", node.Name)
	}
	if !contains(node.Images, image) {
		return store.Node{}, s.missingImage(node, image)
	}
	usedCPU, usedMem, usedDisk, err := s.Store.UsedOnNode(node.ID)
	if err != nil {
		return store.Node{}, err
	}
	if !fits(node, usedCPU, usedMem, usedDisk, cpu, memory, disk) {
		return store.Node{}, s.capacityMessage(node, image)
	}
	return node, nil
}

func fits(n store.Node, usedCPU float64, usedMem, usedDisk int64, cpu float64, mem, disk int64) bool {
	if n.CPU > 0 && usedCPU+cpu > float64(n.CPU)+0.001 {
		return false
	}
	if n.Memory > 0 && usedMem+mem > n.Memory {
		return false
	}
	if n.Disk > 0 && usedDisk+disk > n.Disk {
		return false
	}
	return true
}

func (s *Service) missingImage(node store.Node, image string) error {
	msg := fmt.Sprintf("image %s is not on node %s. run image pull %s", image, node.Name, image)
	if other := s.otherWithImage(image, node.Name); other != "" {
		msg += ". node " + other + " has the image"
	}
	return errors.New(msg)
}

func (s *Service) capacityMessage(node store.Node, image string) error {
	msg := fmt.Sprintf("node %s has no capacity", node.Name)
	if other := s.otherWithRoom(image, node.Name); other != "" {
		msg += ". node " + other + " has the image"
	}
	return errors.New(msg)
}

func (s *Service) otherWithImage(image, except string) string {
	nodes, err := s.Store.NodesWithImage(image)
	if err != nil {
		return ""
	}
	for _, n := range nodes {
		if n.Name != except && s.Online(n) {
			return n.Name
		}
	}
	return ""
}

func (s *Service) otherWithRoom(image, except string) string {
	nodes, err := s.Store.NodesWithImage(image)
	if err != nil {
		return ""
	}
	for _, n := range nodes {
		if n.Name == except || !s.Online(n) {
			continue
		}
		return n.Name
	}
	return ""
}

func (s *Service) Delete(ctx context.Context, name string) error {
	c, err := s.needComputer(name)
	if err != nil {
		return err
	}
	if !s.computerOnline(c) {
		return fmt.Errorf("node %s is offline", c.NodeName)
	}
	if s.Nodes == nil {
		return fmt.Errorf("node %s is unreachable", c.NodeName)
	}
	if err := s.Nodes.Delete(ctx, c.NodeID, name); err != nil && err.Error() != "not found" {
		return s.nodeErr(c.NodeName, err)
	}
	return s.Store.DeleteComputer(name)
}

func (s *Service) Restart(ctx context.Context, name string) error {
	c, err := s.needComputer(name)
	if err != nil {
		return err
	}
	if !s.computerOnline(c) {
		return fmt.Errorf("node %s is offline", c.NodeName)
	}
	img, err := s.Store.Image(c.Image)
	if err != nil {
		return fmt.Errorf("image %s is not registered", c.Image)
	}
	body, err := s.createBody(c.Name, img.Ref, c.CPU, c.Memory, c.Disk)
	if err != nil {
		return err
	}
	if err := s.Nodes.Restart(ctx, c.NodeID, body); err != nil {
		return s.nodeErr(c.NodeName, err)
	}
	return nil
}

func (s *Service) Rename(ctx context.Context, oldName, newName string) error {
	if err := ident.Computer(newName); err != nil {
		return err
	}
	c, err := s.needComputer(oldName)
	if err != nil {
		return err
	}
	if _, err := s.Store.Computer(newName); err == nil {
		return fmt.Errorf("%s already exists", newName)
	}
	if !s.computerOnline(c) {
		return fmt.Errorf("node %s is offline", c.NodeName)
	}
	if err := s.Nodes.Rename(ctx, c.NodeID, oldName, newName); err != nil {
		return s.nodeErr(c.NodeName, err)
	}
	return s.Store.RenameComputer(oldName, newName)
}

type ResizeInput struct {
	Name   string
	CPU    *float64
	Memory *int64
	Disk   *int64
}

func (s *Service) Resize(ctx context.Context, in ResizeInput) error {
	c, err := s.needComputer(in.Name)
	if err != nil {
		return err
	}
	if in.CPU == nil && in.Memory == nil && in.Disk == nil {
		return errors.New("nothing to change")
	}
	cpu, mem, disk := c.CPU, c.Memory, c.Disk
	if in.CPU != nil {
		cpu = *in.CPU
	}
	if in.Memory != nil {
		mem = *in.Memory
	}
	if in.Disk != nil {
		if *in.Disk < c.Disk {
			return errors.New("disk only grows")
		}
		disk = *in.Disk
	}
	if !s.computerOnline(c) {
		return fmt.Errorf("node %s is offline", c.NodeName)
	}
	img, err := s.Store.Image(c.Image)
	if err != nil {
		return fmt.Errorf("image %s is not registered", c.Image)
	}
	body := rpc.ResizeBody{Name: c.Name, CPU: in.CPU, Memory: in.Memory, Disk: in.Disk, ImageRef: img.Ref}
	if err := s.Nodes.Resize(ctx, c.NodeID, body); err != nil {
		if err.Error() == "disk only grows" {
			return err
		}
		if err.Error() == rpc.ErrNoCapacity {
			return s.capacityMessage(store.Node{Name: c.NodeName}, c.Image)
		}
		return s.nodeErr(c.NodeName, err)
	}
	return s.Store.ResizeComputer(c.Name, cpu, mem, disk)
}

type StatView struct {
	CPU     float64 `json:"cpu"`
	Memory  int64   `json:"memory"`
	Disk    int64   `json:"disk"`
	RX      int64   `json:"rx,omitempty"`
	TX      int64   `json:"tx,omitempty"`
	Network string  `json:"network,omitempty"`
}

func (s *Service) Stat(ctx context.Context, name string) (StatView, error) {
	c, err := s.needComputer(name)
	if err != nil {
		return StatView{}, err
	}
	view := StatView{CPU: c.CPU, Memory: c.Memory, Disk: c.Disk}
	if !s.computerOnline(c) || s.Nodes == nil {
		view.Network = "unavailable"
		return view, nil
	}
	st, err := s.Nodes.Stat(ctx, c.NodeID, name)
	if err != nil {
		view.Network = "unavailable"
		return view, nil
	}
	view.CPU, view.Memory, view.Disk = st.CPU, st.Memory, st.Disk
	if st.HasNet {
		view.RX, view.TX = st.RX, st.TX
		view.Network = "ok"
	} else {
		view.Network = "unavailable"
	}
	return view, nil
}

// HostPublicKey is the container sshd host key, for the second handshake.
func (s *Service) HostPublicKey(ctx context.Context, name string) (string, error) {
	c, err := s.needComputer(name)
	if err != nil {
		return "", err
	}
	if !s.computerOnline(c) {
		return "", fmt.Errorf("node %s is offline", c.NodeName)
	}
	if s.Nodes == nil {
		return "", fmt.Errorf("node %s is unreachable", c.NodeName)
	}
	line, err := s.Nodes.HostKey(ctx, c.NodeID, name)
	if err != nil {
		return "", s.nodeErr(c.NodeName, err)
	}
	return line, nil
}

func (s *Service) NodeID(name string) (string, error) {
	c, err := s.needComputer(name)
	if err != nil {
		return "", err
	}
	return c.NodeID, nil
}

type PortalView struct {
	Label string `json:"label"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
}

func (s *Service) CheckPortal(label string) (free bool, holder string, err error) {
	if err := ident.Label(label); err != nil {
		return false, "", err
	}
	p, err := s.Store.PortalByHost(ident.Hostname(label, s.Domain))
	if errors.Is(err, store.ErrNotFound) {
		return true, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return false, p.Container, nil
}

func (s *Service) ClaimPortal(container, label string, port int) (string, error) {
	if err := ident.Label(label); err != nil {
		return "", err
	}
	if port < 1 || port > 65535 {
		return "", errors.New("invalid port")
	}
	c, err := s.needComputer(container)
	if err != nil {
		return "", err
	}
	host := ident.Hostname(label, s.Domain)
	err = s.Store.ClaimPortal(store.Portal{
		Hostname: host, Label: label, Container: container, NodeID: c.NodeID, Port: port,
	})
	var held *store.HeldError
	if errors.As(err, &held) {
		return "", errors.New(held.Error())
	}
	if err != nil {
		return "", err
	}
	return "http://" + host, nil
}

func (s *Service) ListPortals(container string) ([]PortalView, error) {
	if _, err := s.needComputer(container); err != nil {
		return nil, err
	}
	list, err := s.Store.PortalsByContainer(container)
	if err != nil {
		return nil, err
	}
	out := make([]PortalView, 0, len(list))
	for _, p := range list {
		out = append(out, PortalView{Label: p.Label, Host: p.Hostname, Port: p.Port})
	}
	return out, nil
}

func (s *Service) RemovePortal(container, label string) error {
	if err := ident.Label(label); err != nil {
		return err
	}
	if err := s.Store.DeletePortal(container, label); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%s is not claimed", label)
		}
		return err
	}
	return nil
}

// PortalsForNode is what a node should route. The claim already exists on the server.
func (s *Service) PortalsForNode(nodeID string) ([]PortalView, error) {
	comps, err := s.Store.ListComputers()
	if err != nil {
		return nil, err
	}
	var out []PortalView
	for _, c := range comps {
		if c.NodeID != nodeID {
			continue
		}
		list, err := s.Store.PortalsByContainer(c.Name)
		if err != nil {
			return nil, err
		}
		for _, p := range list {
			out = append(out, PortalView{Label: p.Label, Host: p.Hostname, Port: p.Port})
		}
	}
	return out, nil
}

// JoinNode consumes a pairing code and returns a new node id and token.
// The token is shown once to the node process. It is stored for STCP.
func (s *Service) JoinNode(name, code string) (id, token, frpToken string, err error) {
	if err := ident.Node(name); err != nil {
		return "", "", "", err
	}
	if _, err := s.Store.NodeByName(name); err == nil {
		return "", "", "", fmt.Errorf("node %s already exists", name)
	}
	if err := s.ConsumeNode(code); err != nil {
		return "", "", "", err
	}
	id, err = randID()
	if err != nil {
		return "", "", "", err
	}
	token, err = randID()
	if err != nil {
		return "", "", "", err
	}
	// Node tokens are longer than ids.
	extra, err := randID()
	if err != nil {
		return "", "", "", err
	}
	token += extra
	if err := s.Store.CreateNode(id, name, token); err != nil {
		return "", "", "", err
	}
	frpToken, _, err = s.Store.Meta("frp_token")
	if err != nil {
		return "", "", "", err
	}
	return id, token, frpToken, nil
}

// EnsureFRPToken creates the shared frps token once.
func (s *Service) EnsureFRPToken() (string, error) {
	if v, ok, err := s.Store.Meta("frp_token"); err != nil {
		return "", err
	} else if ok && v != "" {
		return v, nil
	}
	tok, err := randID()
	if err != nil {
		return "", err
	}
	extra, err := randID()
	if err != nil {
		return "", err
	}
	tok += extra
	if err := s.Store.SetMeta("frp_token", tok); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *Service) FRPToken() (string, error) {
	v, ok, err := s.Store.Meta("frp_token")
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("frp token is not available")
	}
	return v, nil
}

func randID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *Service) needComputer(name string) (store.Computer, error) {
	c, err := s.Store.Computer(name)
	if errors.Is(err, store.ErrNotFound) {
		return store.Computer{}, fmt.Errorf("computer %s not found", name)
	}
	return c, err
}

func (s *Service) computerOnline(c store.Computer) bool {
	return s.Online(store.Node{LastHeartbeat: c.Heartbeat})
}

func (s *Service) missingNode(err error, name string) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("node %s is not paired", name)
	}
	return err
}

func (s *Service) nodeErr(name string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnreachable) {
		return fmt.Errorf("node %s is unreachable", name)
	}
	return err
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
