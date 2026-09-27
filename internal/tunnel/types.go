// Package tunnel is the QUIC session between a server and a computer.
// The computer dials. One client stream carries newline-JSON control frames.
// The server opens ssh and portal streams.
package tunnel

// ProtocolVersion is the only control version this package speaks.
const ProtocolVersion = 1

const alpn = "box"

const (
	OpHello       = "hello"
	OpPortalCheck = "portal_check"
	OpPortalAdd   = "portal_add"
	OpPortalRm    = "portal_rm"
	OpPortalLs    = "portal_ls"
	OpKeys        = "keys"
	OpEnv         = "env"
	OpStat        = "stat"
)

const (
	KindSSH    = "ssh"
	KindPortal = "portal"
)

// Identity is the computer side of a hello.
type Identity struct {
	Name, Token, User, HostKey, AgentVersion string
}

// HelloResult is the server's reply to a successful hello.
type HelloResult struct {
	Domain string `json:"domain"`
}

// HelloBody is the first control frame, sent by the computer.
type HelloBody struct {
	Version      int    `json:"version"`
	Name         string `json:"name"`
	Token        string `json:"token"`
	User         string `json:"user"`
	HostKey      string `json:"host_key"`
	AgentVersion string `json:"agent_version"`
}

// PortalCheckRequest asks whether a label is free.
type PortalCheckRequest struct {
	Label string `json:"label"`
}

// PortalCheckResponse names the holder when the label is taken.
type PortalCheckResponse struct {
	Free   bool   `json:"free"`
	Holder string `json:"holder"`
}

// PortalAddRequest claims a label for a TCP port on the computer.
type PortalAddRequest struct {
	Label string `json:"label"`
	Port  int    `json:"port"`
}

// PortalAddResponse is the public address of a claim.
type PortalAddResponse struct {
	URL  string `json:"url"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

// PortalRmRequest releases a label.
type PortalRmRequest struct {
	Label string `json:"label"`
}

// Portal is one claim.
type Portal struct {
	Label string `json:"label"`
	Port  int    `json:"port"`
}

// PortalList is the computer's claims.
type PortalList struct {
	Portals []Portal `json:"portals"`
}

// KeysRequest replaces the computer's authorized keys.
type KeysRequest struct {
	AuthorizedKeys []string `json:"authorized_keys"`
}

// EnvRequest replaces the computer's environment.
// Values must not be logged.
type EnvRequest struct {
	Vars map[string]string `json:"vars"`
}

// StatRequest asks the computer for its live load. The body is empty.
type StatRequest struct{}

// StatResponse is the computer's live load.
// CPU is the 1-minute load average. Memory and Disk are used bytes. Uptime is seconds.
type StatResponse struct {
	CPU    float64 `json:"cpu"`
	Memory int64   `json:"memory"`
	Disk   int64   `json:"disk"`
	Uptime int64   `json:"uptime"`
}
