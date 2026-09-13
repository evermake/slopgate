package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Rule is one parsed rule file from .slopgate/rules/*.md.
type Rule struct {
	Name        string   // file basename without .md
	Path        string   // full path to the rule file
	AlwaysCheck bool     // alwaysCheck frontmatter key
	Globs       []string // globs frontmatter key, normalized to a list
	Body        string   // markdown after the frontmatter, whitespace-trimmed
}

// LoadRules reads every *.md file in rulesDir as a rule, sorted by Name for
// stable ordering. A missing rulesDir is normal and yields (nil, nil). A
// malformed rule file returns an error naming the file; it is never
// silently skipped, since a rule that silently stops being enforced is the
// worst failure mode this tool has.
func LoadRules(rulesDir string) ([]Rule, error) {
	entries, err := os.ReadDir(rulesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading rules dir %s: %w", rulesDir, err)
	}

	var rules []Rule
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		path := filepath.Join(rulesDir, name)
		rule, err := loadRuleFile(path)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}

	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })

	return rules, nil
}

func loadRuleFile(path string) (Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Rule{}, fmt.Errorf("rule file %s: %w", path, err)
	}

	name := strings.TrimSuffix(filepath.Base(path), ".md")

	fm, body, err := parseFrontmatter(data)
	if err != nil {
		return Rule{}, fmt.Errorf("rule file %s: %w", path, err)
	}

	return Rule{
		Name:        name,
		Path:        path,
		AlwaysCheck: fm.AlwaysCheck,
		Globs:       []string(fm.Globs),
		Body:        body,
	}, nil
}

// Matches reports whether the rule should run given the set of changed
// files, per the priority order:
//  1. alwaysCheck: true -> always runs, regardless of globs.
//  2. otherwise, if globs is non-empty -> runs iff at least one changed
//     file matches at least one pattern.
//  3. if neither key is present -> the rule always runs. An unscoped rule
//     is unscoped, not dead.
func (r Rule) Matches(changedFiles []string) bool {
	if r.AlwaysCheck {
		return true
	}

	if len(r.Globs) == 0 {
		return true
	}

	for _, f := range changedFiles {
		for _, pattern := range r.Globs {
			if ok, err := doublestar.Match(pattern, f); err == nil && ok {
				return true
			}
		}
	}

	return false
}

// MatchRules filters rules to those that Matches changedFiles, preserving
// order.
func MatchRules(rules []Rule, changedFiles []string) []Rule {
	var matched []Rule
	for _, r := range rules {
		if r.Matches(changedFiles) {
			matched = append(matched, r)
		}
	}
	return matched
}
