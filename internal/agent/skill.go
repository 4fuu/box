package agent

import (
	"os"
	"path/filepath"
	"strings"

	_ "embed"
)

//go:embed skill/SKILL.md
var skillMD string

// installSkill writes the embedded skill for an agent on this machine.
// A failure must not stop the computer agent. Tests set HOME to a temp
// directory; the file then lands under stateDir instead of a real home.
func installSkill(stateDir string) error {
	root := skillRoot(stateDir)
	path := filepath.Join(root, ".agents", "skills", "box", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(skillMD), 0o644)
}

func skillRoot(stateDir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || inTemp(home) {
		return stateDir
	}
	return home
}

func inTemp(path string) bool {
	tmp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(tmp, abs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
