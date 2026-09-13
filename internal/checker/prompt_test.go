package checker

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildPromptIncludesEverythingTheAgentNeeds(t *testing.T) {
	rule := Rule{
		Name: "no-type-casts",
		Body: "Type assertions (`as X`) bypass the type checker.\nNarrow with a type guard instead.",
	}
	in := Input{
		WorktreeDir:  "/tmp/wt",
		BaseSHA:      "d4e5f6a1111",
		HeadSHA:      "a3f9c212222",
		ChangedFiles: []string{"src/api.ts", "src/auth/login.ts"},
		Diff:         "diff --git a/src/api.ts b/src/api.ts\n@@\n+const user = data as User\n",
	}

	got := BuildPrompt(rule, in)

	for _, want := range []string{
		`<rule name="no-type-casts">`,
		"Type assertions (`as X`) bypass the type checker.",
		"Narrow with a type guard instead.",
		"</rule>",
		"the diff between d4e5f6a1111 and a3f9c212222",
		"src/api.ts",
		"src/auth/login.ts",
		"+const user = data as User",
		"Read, Glob and Grep",
		"ONLY if it appears in one of the files changed above",
		"1-based line number",
		"return an empty findings array",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt is missing %q\n---\n%s", want, got)
		}
	}

	// The rule body must sit inside the rule element, and the file list must
	// come before the diff, as in the template.
	bodyIdx := strings.Index(got, "Type assertions")
	openIdx := strings.Index(got, "<rule name=")
	closeIdx := strings.Index(got, "</rule>")
	if !(openIdx < bodyIdx && bodyIdx < closeIdx) {
		t.Errorf("rule body is not inside the <rule> element:\n%s", got)
	}
	if strings.Index(got, "Files changed:") > strings.Index(got, "\nDiff:\n") {
		t.Errorf("changed-file list must precede the diff:\n%s", got)
	}
}

func TestBuildPromptGolden(t *testing.T) {
	got := BuildPrompt(
		Rule{Name: "r", Body: "  body  "},
		Input{BaseSHA: "base", HeadSHA: "head", ChangedFiles: []string{"a.go"}, Diff: "DIFF"},
	)
	want := `You are checking a code change against ONE project rule. Report only violations of
this rule. Ignore every other quality concern.

<rule name="r">
body
</rule>

The change under review is the diff between base and head.

Files changed:
a.go

Diff:
DIFF

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
	if got != want {
		t.Errorf("prompt does not match the template in MVP_PLAN.md\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestBuildPromptEmptyChangeIsStillLegible(t *testing.T) {
	got := BuildPrompt(Rule{Name: "r"}, Input{BaseSHA: "b", HeadSHA: "h"})
	if !strings.Contains(got, "(no files changed)") || !strings.Contains(got, "(empty diff)") {
		t.Errorf("empty input leaves dangling sections:\n%s", got)
	}
}

// A percent sign in the rule body or diff must survive verbatim: the template is
// filled with fmt.Sprintf and a stray verb would corrupt the prompt.
func TestBuildPromptDoesNotInterpretFormatVerbs(t *testing.T) {
	got := BuildPrompt(
		Rule{Name: "r", Body: "never write %s or %d or 100%"},
		Input{BaseSHA: "b", HeadSHA: "h", Diff: "+ fmt.Printf(\"%v\", x)"},
	)
	if !strings.Contains(got, "never write %s or %d or 100%") || !strings.Contains(got, `fmt.Printf("%v", x)`) {
		t.Errorf("format verbs were interpreted:\n%s", got)
	}
	if strings.Contains(got, "%!") {
		t.Errorf("prompt contains a formatting error:\n%s", got)
	}
}

func TestFindingsSchemaIsValidAndMatchesTheFindingShape(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(FindingsSchema), &schema); err != nil {
		t.Fatalf("FindingsSchema is not valid JSON: %v", err)
	}
	props := schema["properties"].(map[string]any)
	items := props["findings"].(map[string]any)["items"].(map[string]any)
	fields := items["properties"].(map[string]any)

	for _, want := range []string{"file", "line", "snippet", "explanation"} {
		if _, ok := fields[want]; !ok {
			t.Errorf("schema is missing the %q field", want)
		}
	}
	// slopgate stamps the rule itself; asking the model for it invites drift.
	if _, ok := fields["rule"]; ok {
		t.Error("schema must not ask the model for the rule name")
	}
	if _, ok := fields["severity"]; ok {
		t.Error("schema must not have a severity field: every violation fails the gate")
	}
	if items["additionalProperties"] != false || schema["additionalProperties"] != false {
		t.Error("schema must set additionalProperties: false")
	}

	// The schema shape and RawFinding must stay in step.
	raw := []byte(`{"findings":[{"file":"a.go","line":3,"snippet":"s","explanation":"e"}]}`)
	var payload struct {
		Findings []RawFinding `json:"findings"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Findings[0] != (RawFinding{File: "a.go", Line: 3, Snippet: "s", Explanation: "e"}) {
		t.Errorf("RawFinding does not decode schema-shaped output: %+v", payload.Findings[0])
	}
}
