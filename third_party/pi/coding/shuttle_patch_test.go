package coding

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShuttleSkillOverride(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ConfigDirName, "skills", "project-skill")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: project-skill\ndescription: Project skill\n---\nInstructions\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if skills := resolveSessionSkills(cwd, SessionOptions{TrustProject: true}); len(skills) == 0 {
		t.Fatal("nil override must retain upstream project skill discovery")
	}
	empty := []Skill{}
	if skills := resolveSessionSkills(cwd, SessionOptions{TrustProject: true, Skills: &empty}); len(skills) != 0 {
		t.Fatalf("explicit empty override leaked discovered skills: %v", skills)
	}
	selected := []Skill{{Name: "host-skill", Description: "Host skill"}}
	skills := resolveSessionSkills(cwd, SessionOptions{TrustProject: true, Skills: &selected})
	if len(skills) != 1 || skills[0].Name != "host-skill" {
		t.Fatalf("override did not supply exactly the host skills: %v", skills)
	}
	skills[0].Name = "changed"
	if selected[0].Name != "host-skill" {
		t.Fatal("resolved skills alias the host's slice")
	}
	loaded, diagnostics := LoadSkillsFromDir(filepath.Dir(dir))
	if len(loaded) != 1 || loaded[0].Name != "project-skill" || len(diagnostics) != 0 {
		t.Fatalf("directory export failed: skills=%v diagnostics=%v", loaded, diagnostics)
	}
}
