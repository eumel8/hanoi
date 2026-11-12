package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, ".hanoi", "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverPrecedenceAndWarnings(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	writeSkill(t, home, "review", "---\nname: review\ndescription: Global review\n---\nGlobal instructions")
	local := writeSkill(t, workspace, "review", "---\nname: review\ndescription: >-\n  Review code\n  carefully.\nmetadata:\n  author: example\n---\nLocal instructions")
	writeSkill(t, home, "alpha", "---\nname: alpha\ndescription: First skill\n---\nAlpha instructions")
	writeSkill(t, workspace, "broken", "not a skill")
	got, warnings := Discover(home, workspace)
	if len(got) != 2 || len(warnings) != 1 {
		t.Fatalf("skills=%v warnings=%v", got, warnings)
	}
	if got[0].Name != "alpha" || got[1].Path != local || got[1].Description != "Review code carefully." {
		t.Fatalf("sorting, local precedence or YAML parsing failed: %v", got)
	}
	if prompt := got[1].Prompt(); !strings.Contains(prompt, "Local instructions") || strings.Contains(prompt, "Global instructions") || !strings.Contains(prompt, filepath.Dir(local)) {
		t.Fatalf("unexpected prompt: %s", prompt)
	}
}

func TestLoadValidation(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"missing frontmatter", "Instructions"},
		{"unclosed frontmatter", "---\nname: review"},
		{"invalid YAML", "---\nname: [\n---\nInstructions"},
		{"directory mismatch", "---\nname: other\ndescription: Review\n---\nInstructions"},
		{"missing description", "---\nname: review\n---\nInstructions"},
		{"empty body", "---\nname: review\ndescription: Review\n---\n"},
		{"oversized file", strings.Repeat("x", 1024*1024+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeSkill(t, t.TempDir(), "review", tc.content)
			if _, err := Load(path); err == nil {
				t.Fatal("accepted invalid skill")
			}
		})
	}
	path := writeSkill(t, t.TempDir(), "review", "\ufeff---\r\nname: review\r\ndescription: 'Review: code'\r\n---\r\nInstructions\r\n")
	if skill, err := Load(path); err != nil || skill.Body != "Instructions" {
		t.Fatalf("Windows line endings: skill=%v err=%v", skill, err)
	}
}

func TestDiscoverMissingDirectories(t *testing.T) {
	got, warnings := Discover(t.TempDir(), t.TempDir())
	if len(got) != 0 || len(warnings) != 0 {
		t.Fatalf("missing directories: skills=%v warnings=%v", got, warnings)
	}
}
