package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// FindFile selects the first workspace config, or uses the explicit path.
func FindFile(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	for _, path := range []string{"hanoi.json", "hanoi.jsonc", "opencode.json", "opencode.jsonc"} {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", nil
}

var envPattern = regexp.MustCompile(`\{env:([^}]+)\}`)

// ExpandEnv resolves OpenCode-style environment references, rejecting unset values.
func ExpandEnv(value string) (string, error) {
	var missing string
	value = envPattern.ReplaceAllStringFunc(value, func(match string) string {
		name := envPattern.FindStringSubmatch(match)[1]
		value, ok := os.LookupEnv(name)
		if !ok {
			missing = name
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return value, nil
}

func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}
