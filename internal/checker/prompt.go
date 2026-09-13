package checker

import (
	"fmt"
	"strings"
)

// FindingsSchema is the JSON schema handed to `claude --json-schema`. It is the
// schema in MVP_PLAN.md § Schema, verbatim.
//
// Note the absence of a `rule` field: slopgate knows which rule it invoked and
// stamps the name on the way out (see ValidateFindings). Never ask the model for
// information slopgate already holds.
const FindingsSchema = `{
  "type": "object",
  "properties": {
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "file":        { "type": "string" },
          "line":        { "type": "integer" },
          "snippet":     { "type": "string" },
          "explanation": { "type": "string" }
        },
        "required": ["file", "line", "snippet", "explanation"],
        "additionalProperties": false
      }
    }
  },
  "required": ["findings"],
  "additionalProperties": false
}`

// promptTemplate is the template in MVP_PLAN.md § Prompt template, verbatim.
// The placeholders are filled by BuildPrompt in order:
//
//	name, rule body, base sha, head sha, changed files, diff
const promptTemplate = `You are checking a code change against ONE project rule. Report only violations of
this rule. Ignore every other quality concern.

<rule name="%s">
%s
</rule>

The change under review is the diff between %s and %s.

Files changed:
%s

Diff:
%s

You may use Read, Glob and Grep to inspect the wider repository for context before
deciding. Surrounding code often determines whether something is actually a violation.

Rules for reporting:
- Report a violation ONLY if it appears in one of the files changed above.
  Pre-existing violations in untouched files are out of scope.
- Every finding must cite the exact file path, the 1-based line number, and a verbatim
  snippet copied from that file. A finding whose snippet does not appear at that
  location will be discarded.
- Explain what the rule requires and how this code departs from it. Be specific.
- If there are no violations, return an empty findings array. Finding nothing is a
  normal and expected outcome.
`

// BuildPrompt renders the rule-check prompt for one rule and one change.
func BuildPrompt(rule Rule, in Input) string {
	changed := strings.Join(in.ChangedFiles, "\n")
	if strings.TrimSpace(changed) == "" {
		changed = "(no files changed)"
	}
	diff := in.Diff
	if strings.TrimSpace(diff) == "" {
		diff = "(empty diff)"
	}
	return fmt.Sprintf(promptTemplate,
		rule.Name,
		strings.TrimSpace(rule.Body),
		in.BaseSHA,
		in.HeadSHA,
		changed,
		diff,
	)
}
