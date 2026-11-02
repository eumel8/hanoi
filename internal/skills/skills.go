// Package skills discovers and loads local Agent Skills.
package skills

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Path        string `yaml:"-"`
	Body        string `yaml:"-"`
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Discover searches global skills first, then lets workspace skills override
// matching names. Invalid files are reported without hiding usable skills.
func Discover(home, workspace string) ([]Skill, []string) {
	byName := make(map[string]Skill)
	var warnings []string
	for _, base := range []string{home, workspace} {
		root := filepath.Join(base, ".hanoi", "skills")
		entries, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", root, err))
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			path := filepath.Join(root, entry.Name(), "SKILL.md")
			skill, err := Load(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				continue
			}
			byName[skill.Name] = skill
		}
	}
	result := make([]Skill, 0, len(byName))
	for _, skill := range byName {
		result = append(result, skill)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, warnings
}

func Load(path string) (Skill, error) {
	file, err := os.Open(path)
	if err != nil {
		return Skill{}, err
	}
	defer file.Close()
	// Bound discovery memory even if a skill accidentally points at a large file.
	const maxSize = 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return Skill{}, err
	}
	if len(data) > maxSize || !utf8.Valid(data) {
		return Skill{}, fmt.Errorf("SKILL.md must be UTF-8 and at most 1 MiB")
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return Skill{}, fmt.Errorf("missing YAML frontmatter")
	}
	end := 1
	for end < len(lines) && lines[end] != "---" {
		end++
	}
	if end == len(lines) {
		return Skill{}, fmt.Errorf("unterminated YAML frontmatter")
	}
	var skill Skill
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &skill); err != nil {
		return Skill{}, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	if len(skill.Name) > 64 || !skillName.MatchString(skill.Name) || skill.Name != filepath.Base(filepath.Dir(path)) {
		return Skill{}, fmt.Errorf("name must match its directory and use 1–64 lowercase letters, digits or single hyphens")
	}
	if strings.TrimSpace(skill.Description) == "" || utf8.RuneCountInString(skill.Description) > 1024 {
		return Skill{}, fmt.Errorf("description must contain 1–1024 characters")
	}
	skill.Path, err = filepath.Abs(path)
	if err != nil {
		return Skill{}, err
	}
	skill.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	if skill.Body == "" {
		return Skill{}, fmt.Errorf("skill instructions are empty")
	}
	return skill, nil
}

func (s Skill) Prompt() string {
	return fmt.Sprintf("Use the %s skill for this task.\n\nSkill file: %s\nBase directory for relative file references: %s\n\n%s", s.Name, s.Path, filepath.Dir(s.Path), s.Body)
}
