package agent

import (
	"os"
	"path/filepath"

	_ "embed"
)

//go:embed skill/SKILL.md
var skillMD string

// installSkill writes the embedded skill. A failure must not stop the agent.
func installSkill(stateDir string) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = stateDir
	}
	path := filepath.Join(home, ".agents", "skills", "box", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(skillMD), 0o644)
}
