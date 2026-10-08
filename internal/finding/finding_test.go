package finding

import (
	"bytes"
	"slices"
	"testing"
)

func TestPointer(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		tokens []string
		want   string
	}{
		{"root only", "#", nil, "#"},
		{"single token", "#", []string{"data"}, "#/data"},
		{"nested", "#", []string{"data", "user", "first_name"}, "#/data/user/first_name"},
		{"slash escaped", "#", []string{"paths", "/v1/orders", "get"}, "#/paths/~1v1~1orders/get"},
		{"tilde escaped", "#", []string{"a~b"}, "#/a~0b"},
		{"tilde before slash", "#", []string{"~/"}, "#/~0~1"},
		{"existing base", "#/data", []string{"id"}, "#/data/id"},
		{"base not re-escaped", "#/paths/~1v1", []string{"get"}, "#/paths/~1v1/get"},
		{"empty token", "#", []string{""}, "#/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Pointer(tt.base, tt.tokens...); got != tt.want {
				t.Errorf("Pointer(%q, %q) = %q, want %q", tt.base, tt.tokens, got, tt.want)
			}
		})
	}
}

func TestSort(t *testing.T) {
	in := []Finding{
		{RuleID: "B", Location: "#/b", Line: 2},
		{RuleID: "A", Location: "#/b", Line: 2},
		{RuleID: "Z", Location: "#/a", Line: 2},
		{RuleID: "Y", Location: "#/z", Line: 1},
		{RuleID: "X", Location: "#/a", Line: 2, Message: "first"},
		{RuleID: "X", Location: "#/a", Line: 2, Message: "second"},
	}
	want := []Finding{
		{RuleID: "Y", Location: "#/z", Line: 1},
		{RuleID: "X", Location: "#/a", Line: 2, Message: "first"},
		{RuleID: "X", Location: "#/a", Line: 2, Message: "second"},
		{RuleID: "Z", Location: "#/a", Line: 2},
		{RuleID: "A", Location: "#/b", Line: 2},
		{RuleID: "B", Location: "#/b", Line: 2},
	}
	Sort(in)
	if !slices.Equal(in, want) {
		t.Errorf("Sort result =\n%v\nwant\n%v", in, want)
	}
}

func TestFailed(t *testing.T) {
	must := Finding{Level: Must}
	should := Finding{Level: Should}
	tests := []struct {
		name   string
		fs     []Finding
		strict bool
		want   bool
	}{
		{"none", nil, false, false},
		{"none strict", nil, true, false},
		{"should only", []Finding{should}, false, false},
		{"should only strict", []Finding{should}, true, true},
		{"must", []Finding{should, must}, false, true},
		{"must strict", []Finding{must}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Failed(tt.fs, tt.strict); got != tt.want {
				t.Errorf("Failed = %v, want %v", got, tt.want)
			}
		})
	}
}

var sample = []Finding{
	{RuleID: "R1", Level: Must, Location: "#/paths/~1v1~1orders/get", Line: 12, Message: "missing x"},
	{RuleID: "R2", Level: Should, Location: "#/data/user", Message: "prefer y"},
}

func TestWriteText(t *testing.T) {
	tests := []struct {
		name string
		fs   []Finding
		want string
	}{
		{"none", nil, "api.yaml: no findings\n"},
		{"findings", sample,
			"api.yaml:12: MUST R1 #/paths/~1v1~1orders/get: missing x\n" +
				"api.yaml: SHOULD R2 #/data/user: prefer y\n" +
				"2 finding(s): 1 MUST, 1 SHOULD\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteText(&buf, "api.yaml", tt.fs); err != nil {
				t.Fatal(err)
			}
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	tests := []struct {
		name string
		fs   []Finding
		want string
	}{
		{"nil is empty array", nil, "{\n  \"source\": \"api.yaml\",\n  \"findings\": []\n}\n"},
		{"findings", sample, `{
  "source": "api.yaml",
  "findings": [
    {
      "ruleId": "R1",
      "level": "MUST",
      "location": "#/paths/~1v1~1orders/get",
      "line": 12,
      "message": "missing x"
    },
    {
      "ruleId": "R2",
      "level": "SHOULD",
      "location": "#/data/user",
      "message": "prefer y"
    }
  ]
}
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteJSON(&buf, "api.yaml", tt.fs); err != nil {
				t.Fatal(err)
			}
			if got := buf.String(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
