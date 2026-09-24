package rpc

// CreateBody is the server's request to make a computer.
// Env is injected into the container. Do not log this struct.
type CreateBody struct {
	Name           string            `json:"name"`
	ImageRef       string            `json:"image_ref"`
	CPU            float64           `json:"cpu"`
	Memory         int64             `json:"memory"`
	Disk           int64             `json:"disk"`
	Env            map[string]string `json:"env,omitempty"`
	AuthorizedKeys []string          `json:"authorized_keys"`
}

type ResizeBody struct {
	Name     string   `json:"name"`
	CPU      *float64 `json:"cpu,omitempty"`
	Memory   *int64   `json:"memory,omitempty"`
	Disk     *int64   `json:"disk,omitempty"`
	ImageRef string   `json:"image_ref,omitempty"`
}

type NameBody struct {
	Name string `json:"name"`
}

type RenameBody struct {
	Old string `json:"old"`
	New string `json:"new"`
}

type PullBody struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

type KeysBody struct {
	AuthorizedKeys []string `json:"authorized_keys"`
}

type HostKeyBody struct {
	Public string `json:"public"`
}

type StatBody struct {
	CPU     float64 `json:"cpu"`
	Memory  int64   `json:"memory"`
	Disk    int64   `json:"disk"`
	RX      int64   `json:"rx"`
	TX      int64   `json:"tx"`
	HasNet  bool    `json:"has_net"`
	Running bool    `json:"running"`
}

type PortalBody struct {
	Label string `json:"label"`
	Port  int    `json:"port,omitempty"`
}

type PortalResult struct {
	Free   bool   `json:"free"`
	Holder string `json:"holder,omitempty"`
	URL    string `json:"url,omitempty"`
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port,omitempty"`
}

type PortalList struct {
	Portals []PortalItem `json:"portals"`
}

type PortalItem struct {
	Label string `json:"label"`
	Port  int    `json:"port"`
}

type DomainBody struct {
	Domain string `json:"domain"`
}

const (
	OpCreate   = "create"
	OpDelete   = "delete"
	OpRestart  = "restart"
	OpRename   = "rename"
	OpResize   = "resize"
	OpPull     = "pull"
	OpSyncKeys = "sync_keys"
	OpHostKey  = "hostkey"
	OpStat     = "stat"

	OpDomain      = "domain"
	OpPortalCheck = "portal_check"
	OpPortalAdd   = "portal_add"
	OpPortalLs    = "portal_ls"
	OpPortalRm    = "portal_rm"
)

// ErrNoCapacity is the text a node returns when a computer does not fit.
const ErrNoCapacity = "no capacity"
