//go:build windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sshdBehavior for Windows: the OpenSSH Server optional capability keeps
// its config and host keys under ProgramData. Writing the config and
// restarting the service need an elevated PowerShell; there is no reload,
// so a restart drops active sessions.
var sshdPlatform = sshdBehavior{
	config:    filepath.Join(programData, "ssh", "sshd_config"),
	manual:    "sshd_config is not writable. In an elevated PowerShell add these lines, then restart sshd:\n  " + sshdKeysLine + "\n  AcceptEnv *\n  Restart-Service sshd\n",
	reloadTip: "restart sshd: Restart-Service sshd in an elevated PowerShell",
	reloaded:  "restarted sshd",
	reload: func() bool {
		return exec.Command("powershell", "-NoProfile", "-Command", "Restart-Service sshd").Run() == nil
	},
	missing: func() string {
		return fmt.Sprintf("OpenSSH Server is not installed. In an elevated PowerShell:\n"+
			"  Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0\n"+
			"  Set-Service sshd -StartupType Automatic\n"+
			"  Start-Service sshd\n"+
			"then run box join again. The config lives in %s.\n",
			filepath.Join(programData, "ssh", "sshd_config"))
	},
	// Windows sshd's default config ends with a Match Group administrators
	// block that reads keys from administrators_authorized_keys instead of
	// ~/.ssh. Box cannot write that file as a normal user, so say so.
	adminNote: func(config string) string {
		body, err := os.ReadFile(config)
		if err != nil || !strings.Contains(string(body), "administrators_authorized_keys") {
			return ""
		}
		return "note: accounts in the administrators group authorize via " +
			filepath.Join(programData, "ssh", "administrators_authorized_keys") +
			",\nnot ~/.ssh. Copy the keys there or remove the Match Group administrators block.\n"
	},
}
