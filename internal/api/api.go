// Package api is the JSON the node exchanges with the server over HTTP.
// Env values are not part of this API. Create carries them on the node RPC instead.
package api

const Prefix = "/box/node/v1"

type JoinRequest struct {
	Name string `json:"name"`
	Code string `json:"code"`
}

type JoinResponse struct {
	ID       string `json:"id"`
	Token    string `json:"token"`
	FRPToken string `json:"frp_token"`
}

type HeartbeatRequest struct {
	CPU        int              `json:"cpu"`
	Memory     int64            `json:"memory"`
	Disk       int64            `json:"disk"`
	UsedCPU    float64          `json:"used_cpu"`
	UsedMemory int64            `json:"used_memory"`
	UsedDisk   int64            `json:"used_disk"`
	Images     []string         `json:"images"`
	Computers  []ComputerReport `json:"computers"`
}

type ComputerReport struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type HeartbeatResponse struct {
	Domain  string         `json:"domain"`
	Keys    []string       `json:"keys"`
	Portals []PortalReport `json:"portals"`
}

type PortalReport struct {
	Label     string `json:"label"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Container string `json:"container"`
}

type ClaimRequest struct {
	Container string `json:"container"`
	Label     string `json:"label"`
	Port      int    `json:"port"`
}

type ClaimResponse struct {
	URL  string `json:"url"`
	Host string `json:"host"`
}

type CheckRequest struct {
	Label string `json:"label"`
}

type CheckResponse struct {
	Free   bool   `json:"free"`
	Holder string `json:"holder,omitempty"`
}

type ReleaseRequest struct {
	Container string `json:"container"`
	Label     string `json:"label"`
}

type DomainResponse struct {
	Domain string `json:"domain"`
}
