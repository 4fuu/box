package agent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/4fuu/box/internal/rpc"
	"golang.org/x/crypto/ssh"
)

// sshdProbeTimeout bounds the local probe. sshd answers its identification
// line immediately; a machine that does not is not ready for a splice.
const sshdProbeTimeout = 5 * time.Second

// enableSSHD runs the sshd config edit, probes the result, and, when the
// probe passes, drops the current tunnel so the next hello carries hostKey.
// A failed probe leaves SSH disabled. The result holds no secrets.
func (a *agent) enableSSHD() rpc.SSHDEnableResult {
	var out bytes.Buffer
	ensureSSHD(&out)
	res := rpc.SSHDEnableResult{Output: out.String()}
	if err := probeSSHD(); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Enabled = true
	a.mu.Lock()
	sess := a.sess
	a.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
		res.Reconnecting = true
	}
	return res
}

// probeSSHD checks the two things a splice needs: the sshd host public key
// must read and parse as an authorized key, and sshd on sshDialAddr must
// answer with an SSH identification line. It changes nothing.
func probeSSHD() error {
	raw, err := os.ReadFile(sshHostPub)
	if err != nil {
		return fmt.Errorf("sshd host key %s: %v", sshHostPub, err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey(bytes.TrimSpace(raw)); err != nil {
		return fmt.Errorf("sshd host key %s is not usable: %v", sshHostPub, err)
	}
	conn, err := net.DialTimeout("tcp", sshDialAddr, sshdProbeTimeout)
	if err != nil {
		return fmt.Errorf("sshd is not reachable on %s: %v", sshDialAddr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(sshdProbeTimeout))
	if err := readSSHIdentification(conn); err != nil {
		return fmt.Errorf("sshd on %s: %v", sshDialAddr, err)
	}
	return nil
}

// readSSHIdentification waits for a line starting with "SSH-". RFC 4253
// allows other lines before the identification, so they are skipped.
func readSSHIdentification(r io.Reader) error {
	br := bufio.NewReader(io.LimitReader(r, 4096))
	for i := 0; i < 8; i++ {
		line, err := br.ReadString('\n')
		if strings.HasPrefix(line, "SSH-") {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("no SSH identification line")
			}
			return err
		}
	}
	return errors.New("no SSH identification line")
}
