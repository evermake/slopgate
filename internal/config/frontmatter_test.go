package config

import (
	"reflect"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantFM      frontmatter
		wantBody    string
		wantErr     bool
		errContains string
	}{
		{
			name: "globs as single string",
			input: "---\n" +
				"globs: 'src/**'\n" +
				"---\n" +
				"Body text.\n",
			wantFM:   frontmatter{Globs: Globs{"src/**"}},
			wantBody: "Body text.",
		},
		{
			name: "globs as list",
			input: "---\n" +
				"globs:\n" +
				"  - 'src/**'\n" +
				"  - 'test/**/*.go'\n" +
				"---\n" +
				"Body text.\n",
			wantFM:   frontmatter{Globs: Globs{"src/**", "test/**/*.go"}},
			wantBody: "Body text.",
		},
		{
			name: "alwaysCheck true",
			input: "---\n" +
				"alwaysCheck: true\n" +
				"---\n" +
				"Body.\n",
			wantFM:   frontmatter{AlwaysCheck: true},
			wantBody: "Body.",
		},
		{
			name:     "no frontmatter at all",
			input:    "# Just a heading\n\nSome prose.\n",
			wantFM:   frontmatter{},
			wantBody: "# Just a heading\n\nSome prose.",
		},
		{
			name:     "empty frontmatter block",
			input:    "---\n---\nBody.\n",
			wantFM:   frontmatter{},
			wantBody: "Body.",
		},
		{
			name: "leading blank lines before delimiter",
			input: "\n\n  \n---\n" +
				"alwaysCheck: true\n" +
				"---\n" +
				"Body.\n",
			wantFM:   frontmatter{AlwaysCheck: true},
			wantBody: "Body.",
		},
		{
			name: "CRLF line endings",
			input: "---\r\n" +
				"alwaysCheck: true\r\n" +
				"globs:\r\n" +
				"  - 'src/**'\r\n" +
				"---\r\n" +
				"Body line one.\r\nBody line two.\r\n",
			wantFM:   frontmatter{AlwaysCheck: true, Globs: Globs{"src/**"}},
			wantBody: "Body line one.\nBody line two.",
		},
		{
			name: "leading BOM",
			input: "\xEF\xBB\xBF---\n" +
				"alwaysCheck: true\n" +
				"---\n" +
				"Body.\n",
			wantFM:   frontmatter{AlwaysCheck: true},
			wantBody: "Body.",
		},
		{
			name: "malformed YAML",
			input: "---\n" +
				"alwaysCheck: [this is not a bool\n" +
				"---\n" +
				"Body.\n",
			wantErr:     true,
			errContains: "frontmatter",
		},
		{
			name: "unterminated frontmatter",
			input: "---\n" +
				"alwaysCheck: true\n",
			wantErr: true,
		},
		{
			name: "globs wrong type",
			input: "---\n" +
				"globs: 42\n" +
				"---\n" +
				"Body.\n",
			wantErr: true,
		},
		{
			name:     "body has surrounding whitespace trimmed",
			input:    "---\n---\n\n\n  Body with leading/trailing blank lines.  \n\n\n",
			wantFM:   frontmatter{},
			wantBody: "Body with leading/trailing blank lines.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fm, body, err := parseFrontmatter([]byte(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseFrontmatter() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFrontmatter() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(fm, tc.wantFM) {
				t.Errorf("frontmatter = %+v, want %+v", fm, tc.wantFM)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}
