package config

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// utf8BOM is the byte sequence a UTF-8 BOM encodes to.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// frontmatter is the YAML block at the top of a rule file. alwaysCheck and
// globs are the only supported keys.
type frontmatter struct {
	AlwaysCheck bool  `yaml:"alwaysCheck"`
	Globs       Globs `yaml:"globs"`
}

// Globs unmarshals a YAML `globs` value that may be either a single string
// or a list of strings, always normalizing to a list.
type Globs []string

func (g *Globs) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Tag != "!!str" {
			return fmt.Errorf("globs: expected a string or a list of strings, got %s", value.Tag)
		}
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		*g = Globs{s}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*g = Globs(list)
		return nil
	default:
		return fmt.Errorf("globs: expected a string or a list of strings")
	}
}

// parseFrontmatter splits a rule file's raw bytes into its YAML frontmatter
// and Markdown body.
//
// Handles: a leading UTF-8 BOM, CRLF/CR line endings, and blank lines
// preceding the opening "---" delimiter. A file with no "---" delimiter at
// all is valid: no keys are present (so the rule always runs) and the
// entire, whitespace-trimmed file becomes the body. An opening delimiter
// with no matching closing delimiter is malformed and returns an error.
func parseFrontmatter(data []byte) (frontmatter, string, error) {
	data = bytes.TrimPrefix(data, utf8BOM)

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	lines := strings.Split(content, "\n")

	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}

	if i >= len(lines) || strings.TrimSpace(lines[i]) != "---" {
		// No frontmatter delimiter: the whole file is the body.
		return frontmatter{}, strings.TrimSpace(content), nil
	}

	start := i + 1
	j := start
	for j < len(lines) && strings.TrimSpace(lines[j]) != "---" {
		j++
	}
	if j >= len(lines) {
		return frontmatter{}, "", fmt.Errorf("unterminated frontmatter: missing closing '---' delimiter")
	}

	fmYAML := strings.Join(lines[start:j], "\n")
	body := strings.TrimSpace(strings.Join(lines[j+1:], "\n"))

	var fm frontmatter
	if strings.TrimSpace(fmYAML) != "" {
		if err := yaml.Unmarshal([]byte(fmYAML), &fm); err != nil {
			return frontmatter{}, "", fmt.Errorf("invalid frontmatter YAML: %w", err)
		}
	}

	return fm, body, nil
}
