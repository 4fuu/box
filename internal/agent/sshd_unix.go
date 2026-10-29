//go:build unix

package agent

import "os/exec"

// sshdBehavior for Linux: the usual config path, a graceful reload, and
// sudo-printed manual steps.
var sshdPlatform = sshdBehavior{
	config:    "/etc/ssh/sshd_config",
	manual:    "sshd_config is not writable. Add these lines, then reload sshd:\n  " + sshdKeysLine + "\n  AcceptEnv *\n  sudo systemctl reload ssh\n",
	reloadTip: "reload sshd: sudo systemctl reload ssh",
	reloaded:  "reloaded sshd",
	reload: func() bool {
		for _, name := range []string{"ssh", "sshd"} {
			if exec.Command("systemctl", "reload", name).Run() == nil {
				return true
			}
		}
		return false
	},
	// A missing /etc/ssh/sshd_config is covered by the manual text.
	missing:   func() string { return "" },
	adminNote: func(string) string { return "" },
}
