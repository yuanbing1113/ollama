package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/template"
)

func createZipFile(t *testing.T, name string) *os.File {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	zf := zip.NewWriter(f)
	defer zf.Close()

	zh, err := zf.CreateHeader(&zip.FileHeader{Name: name})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(zh, bytes.NewReader([]byte(""))); err != nil {
		t.Fatal(err)
	}

	return f
}

func TestExtractFromZipFile(t *testing.T) {
	cases := []struct {
		name   string
		expect []string
		err    error
	}{
		{
			name:   "good",
			expect: []string{"good"},
		},
		{
			name:   strings.Join([]string{"path", "..", "to", "good"}, string(os.PathSeparator)),
			expect: []string{filepath.Join("to", "good")},
		},
		{
			name:   strings.Join([]string{"path", "..", "to", "..", "good"}, string(os.PathSeparator)),
			expect: []string{"good"},
		},
		{
			name:   strings.Join([]string{"path", "to", "..", "..", "good"}, string(os.PathSeparator)),
			expect: []string{"good"},
		},
		{
			name: strings.Join([]string{"..", "..", "..", "..", "..", "..", "..", "..", "..", "..", "..", "..", "..", "..", "..", "..", "bad"}, string(os.PathSeparator)),
			err:  zip.ErrInsecurePath,
		},
		{
			name: strings.Join([]string{"path", "..", "..", "to", "bad"}, string(os.PathSeparator)),
			err:  zip.ErrInsecurePath,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			f := createZipFile(t, tt.name)
			defer f.Close()

			tempDir := t.TempDir()
			if err := extractFromZipFile(tempDir, f, func(api.ProgressResponse) {}); !errors.Is(err, tt.err) {
				t.Fatal(err)
			}

			var matches []string
			if err := filepath.Walk(tempDir, func(p string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}

				if !fi.IsDir() {
					matches = append(matches, p)
				}

				return nil
			}); err != nil {
				t.Fatal(err)
			}

			var actual []string
			for _, match := range matches {
				rel, err := filepath.Rel(tempDir, match)
				if err != nil {
					t.Error(err)
				}

				actual = append(actual, rel)
			}

			if !slices.Equal(actual, tt.expect) {
				t.Fatalf("expected %d files, got %d", len(tt.expect), len(matches))
			}
		})
	}
}

func readFile(t *testing.T, base, name string) *bytes.Buffer {
	t.Helper()

	bts, err := os.ReadFile(filepath.Join(base, name))
	if err != nil {
		t.Fatal(err)
	}

	return bytes.NewBuffer(bts)
}

func TestExecuteWithTools(t *testing.T) {
	p := filepath.Join("testdata", "tools")
	cases := []struct {
		model  string
		output string
		ok     bool
	}{
		{"mistral", `[TOOL_CALLS]  [{"name": "get_current_weather", "arguments": {"format":"fahrenheit","location":"San Francisco, CA"}},{"name": "get_current_weather", "arguments": {"format":"celsius","location":"Toronto, Canada"}}]`, true},
		{"mistral", `[TOOL_CALLS]  [{"name": "get_current_weather", "arguments": {"format":"fahrenheit","location":"San Francisco, CA"}},{"name": "get_current_weather", "arguments": {"format":"celsius","location":"Toronto, Canada"}}]

The temperature in San Francisco, CA is 70°F and in Toronto, Canada is 20°C.`, true},
		{"mistral", `I'm not aware of that information. However, I can suggest searching for the weather using the "get_current_weather" function:

		[{"name": "get_current_weather", "arguments": {"format":"fahrenheit","location":"San Francisco, CA"}},{"name": "get_current_weather", "arguments": {"format":"celsius","location":"Toronto, Canada"}}]`, true},
		{"mistral", " The weather in San Francisco, CA is 70°F and in Toronto, Canada is 20°C.", false},
		{"command-r-plus", "Action: ```json" + `
[
    {
        "tool_name": "get_current_weather",
        "parameters": {
            "format": "fahrenheit",
            "location": "San Francisco, CA"
        }
    },
    {
        "tool_name": "get_current_weather",
        "parameters": {
            "format": "celsius",
            "location": "Toronto, Canada"
        }
    }
]
` + "```", true},
		{"command-r-plus", " The weather in San Francisco, CA is 70°F and in Toronto, Canada is 20°C.", false},
		{"firefunction", ` functools[{"name": "get_current_weather", "arguments": {"format":"fahrenheit","location":"San Francisco, CA"}},{"name": "get_current_weather", "arguments": {"format":"celsius","location":"Toronto, Canada"}}]`, true},
		{"firefunction", " The weather in San Francisco, CA is 70°F and in Toronto, Canada is 20°C.", false},
		{"llama3-groq-tool-use", `<tool_call>
{"name": "get_current_weather", "arguments": {"format":"fahrenheit","location":"San Francisco, CA"}}
{"name": "get_current_weather", "arguments": {"format":"celsius","location":"Toronto, Canada"}}
</tool_call>`, true},
		{"xlam", `{"tool_calls": [{"name": "get_current_weather", "arguments": {"format":"fahrenheit","location":"San Francisco, CA"}},{"name": "get_current_weather", "arguments": {"format":"celsius","location":"Toronto, Canada"}}]}`, true},
	}

	var tools []api.Tool
	if err := json.Unmarshal(readFile(t, p, "tools.json").Bytes(), &tools); err != nil {
		t.Fatal(err)
	}

	var messages []api.Message
	if err := json.Unmarshal(readFile(t, p, "messages.json").Bytes(), &messages); err != nil {
		t.Fatal(err)
	}

	calls := []api.ToolCall{
		{
			Function: api.ToolCallFunction{
				Name: "get_current_weather",
				Arguments: api.ToolCallFunctionArguments{
					"format":   "fahrenheit",
					"location": "San Francisco, CA",
				},
			},
		},
		{
			Function: api.ToolCallFunction{
				Name: "get_current_weather",
				Arguments: api.ToolCallFunctionArguments{
					"format":   "celsius",
					"location": "Toronto, Canada",
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.model, func(t *testing.T) {
			tmpl, err := template.Parse(readFile(t, p, fmt.Sprintf("%s.gotmpl", tt.model)).String())
			if err != nil {
				t.Fatal(err)
			}

			t.Run("template", func(t *testing.T) {
				var actual bytes.Buffer
				if err := tmpl.Execute(&actual, template.Values{Tools: tools, Messages: messages}); err != nil {
					t.Fatal(err)
				}

				if diff := cmp.Diff(actual.String(), readFile(t, p, fmt.Sprintf("%s.out", tt.model)).String()); diff != "" {
					t.Errorf("mismatch (-got +want):\n%s", diff)
				}
			})

			t.Run("parse", func(t *testing.T) {
				m := &Model{Template: tmpl}
				actual, ok := m.parseToolCalls(tt.output)
				if ok != tt.ok {
					t.Fatalf("expected %t, got %t", tt.ok, ok)
				}

				if tt.ok {
					if diff := cmp.Diff(actual, calls); diff != "" {
						t.Errorf("mismatch (-got +want):\n%s", diff)
					}
				}
			})
		})
	}
}
