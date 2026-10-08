package jsonrules

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/floriankraemer/http-api-spec/validator/internal/finding"
)

func parse(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad test json %q: %v", s, err)
	}
	return v
}

// got renders findings as "RULE LEVEL location" for compact comparison.
func got(fs []finding.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, fmt.Sprintf("%s %s %s", f.RuleID, f.Level, f.Location))
	}
	return out
}

func assertFindings(t *testing.T, fs []finding.Finding, want []string) {
	t.Helper()
	g := got(fs)
	if !slices.Equal(g, want) {
		t.Fatalf("findings mismatch\n got: %q\nwant: %q\nmessages: %v", g, want, messages(fs))
	}
	for _, f := range fs {
		if f.Message == "" || f.Message != strings.ToLower(f.Message[:1])+f.Message[1:] {
			t.Errorf("message must be non-empty and start lowercase: %q", f.Message)
		}
	}
}

func messages(fs []finding.Finding) []string {
	var m []string
	for _, f := range fs {
		m = append(m, f.Message)
	}
	return m
}

func TestIsCamelCase(t *testing.T) {
	tests := map[string]bool{
		"firstName": true, "userID": true, "postId": true, "a": true, "item2": true,
		"ITUser": true, "ID": true, "URL": true,
		"User": false, "FirstName": false, "A": false, "UserID": false,
		"first_name": false, "first-name": false, "2fa": false, "": false,
		"first name": false, "naïve": false, "_links": false,
	}
	for in, want := range tests {
		if IsCamelCase(in) != want {
			t.Errorf("IsCamelCase(%q) = %v, want %v", in, !want, want)
		}
	}
}

func TestIsControlField(t *testing.T) {
	for in, want := range map[string]bool{"_links": true, "_actions": true, "_other": false, "links": false, "": false} {
		if IsControlField(in) != want {
			t.Errorf("IsControlField(%q) want %v", in, want)
		}
	}
}

func TestIsErrorCode(t *testing.T) {
	tests := map[string]bool{
		"0001-0001": true, "TaxCalculator-021": true, "AP-E1": true, "AP-E0012": true,
		"550e8400-e29b-41d4-a716-446655440000": false,
		"nodash":                               false, "-E1": false, "AP-": false, "AP--E1": false,
		"AP-E1-X": false, "AP -E1": false, "AP-E 1": false, " AP-E1": false, "AP-E1\n": false,
		"AP_X-E1": false, "": false, "-": false,
	}
	for in, want := range tests {
		if IsErrorCode(in) != want {
			t.Errorf("IsErrorCode(%q) want %v", in, want)
		}
	}
}

func TestCheckKeys(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []string
	}{
		{"clean", `{"data":{"users":[{"firstName":"a","userID":1}]}}`, nil},
		{"snake case", `{"first_name":1}`, []string{"naming-camel-case MUST #/first_name"}},
		{"kebab", `{"first-name":1}`, []string{"naming-camel-case MUST #/first-name"}},
		{"pascal nested in array", `{"data":{"users":[{"ok":1},{"FirstName":1}]}}`,
			[]string{"naming-camel-case MUST #/data/users/1/FirstName"}},
		{"leading underscore only reports underscore", `{"_id":1}`, []string{"no-underscore-fields MUST #/_id"}},
		{"underscore descends", `{"_x":{"bad_key":1}}`,
			[]string{"no-underscore-fields MUST #/_x", "naming-camel-case MUST #/_x/bad_key"}},
		{"control fields allowed and not descended", `{"_links":{"self_x":{"href":"/"}},"_actions":{"do_it":{}}}`, nil},
		{"control fields inside entity", `{"data":{"orders":[{"_links":{"Self":1},"order_id":2}]}}`,
			[]string{"naming-camel-case MUST #/data/orders/0/order_id"}},
		{"pointer escaping", `{"a/b":1}`, []string{"naming-camel-case MUST #/a~1b"}},
		{"sorted order", `{"b_b":1,"a_a":2}`, []string{"naming-camel-case MUST #/a_a", "naming-camel-case MUST #/b_b"}},
		{"scalar", `5`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertFindings(t, CheckKeys(parse(t, tc.json), "#"), tc.want)
		})
	}
}

func TestCheckPagination(t *testing.T) {
	const base = `"currentPage":1,"totalPages":3,"perPage":10,"totalItems":25`
	tests := []struct {
		name string
		json string
		want []string
	}{
		{"valid", `{` + base + `}`, nil},
		{"valid with optionals", `{` + base + `,"nextPage":2,"previousPage":null}`, nil},
		{"integral float", `{"currentPage":1.0,"totalPages":3,"perPage":10,"totalItems":25}`, nil},
		{"not an object", `[]`, []string{"pagination-shape MUST #/pagination"}},
		{"null", `null`, []string{"pagination-shape MUST #/pagination"}},
		{"missing required", `{"currentPage":1,"totalPages":3,"perPage":10}`, []string{"pagination-shape MUST #/pagination"}},
		{"required string", `{"currentPage":"1","totalPages":3,"perPage":10,"totalItems":25}`,
			[]string{"pagination-shape MUST #/pagination/currentPage"}},
		{"required fraction", `{"currentPage":1.5,"totalPages":3,"perPage":10,"totalItems":25}`,
			[]string{"pagination-shape MUST #/pagination/currentPage"}},
		{"required null", `{"currentPage":null,"totalPages":3,"perPage":10,"totalItems":25}`,
			[]string{"pagination-shape MUST #/pagination/currentPage"}},
		{"optional wrong type", `{` + base + `,"nextPage":"2"}`, []string{"pagination-shape MUST #/pagination/nextPage"}},
		{"unknown", `{` + base + `,"cursor":"x"}`, []string{"pagination-shape MUST #/pagination/cursor"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertFindings(t, CheckPagination(parse(t, tc.json), "#/pagination"), tc.want)
		})
	}
}

func TestCheckHypermedia(t *testing.T) {
	tests := []struct {
		name string
		json string
		want []string
	}{
		{"none", `{"data":{"orders":[{"id":1}]}}`, nil},
		{"valid link", `{"_links":{"self":{"href":"/o/1","title":"Order"}}}`, nil},
		{"valid link array", `{"_links":{"items":[{"href":"/a"},{"href":"/b"}]}}`, nil},
		{"valid action", `{"_actions":{"cancel":{"href":"/o/1/cancel","method":"POST","title":"Cancel","fields":[{"name":"reason","type":"string","required":true,"title":"Reason"}]}}}`, nil},
		{"empty links", `{"_links":{}}`, []string{"hypermedia-shape SHOULD #/_links"}},
		{"empty actions", `{"_actions":{}}`, []string{"hypermedia-shape SHOULD #/_actions"}},
		{"links not object", `{"_links":[]}`, []string{"hypermedia-shape MUST #/_links"}},
		{"actions not object", `{"_actions":"x"}`, []string{"hypermedia-shape MUST #/_actions"}},
		{"relation not camel", `{"_links":{"my_rel":{"href":"/"}}}`, []string{"hypermedia-shape MUST #/_links/my_rel"}},
		{"link extra field", `{"_links":{"self":{"href":"/","method":"GET"}}}`, []string{"hypermedia-shape MUST #/_links/self/method"}},
		{"link missing href", `{"_links":{"self":{"title":"x"}}}`, []string{"hypermedia-shape MUST #/_links/self"}},
		{"link empty href", `{"_links":{"self":{"href":""}}}`, []string{"hypermedia-shape MUST #/_links/self/href"}},
		{"link href number", `{"_links":{"self":{"href":1}}}`, []string{"hypermedia-shape MUST #/_links/self/href"}},
		{"link title number", `{"_links":{"self":{"href":"/","title":1}}}`, []string{"hypermedia-shape MUST #/_links/self/title"}},
		{"link not object", `{"_links":{"self":"/x"}}`, []string{"hypermedia-shape MUST #/_links/self"}},
		{"empty link array", `{"_links":{"items":[]}}`, []string{"hypermedia-shape MUST #/_links/items"}},
		{"bad element in array", `{"_links":{"items":[{"href":"/a"},{"href":2}]}}`, []string{"hypermedia-shape MUST #/_links/items/1/href"}},
		{"nested deep with exact pointer", `{"data":{"orders":[{"id":1},{"_actions":{"cancel":{"href":"/c","method":"post"}}}]}}`,
			[]string{"hypermedia-shape MUST #/data/orders/1/_actions/cancel/method"}},
		{"action method GET", `{"_actions":{"x":{"href":"/c","method":"GET"}}}`, []string{"hypermedia-shape MUST #/_actions/x/method"}},
		{"action missing method and href", `{"_actions":{"x":{}}}`, []string{"hypermedia-shape MUST #/_actions/x", "hypermedia-shape MUST #/_actions/x"}},
		{"action name not camel", `{"_actions":{"Do_It":{"href":"/","method":"PUT"}}}`, []string{"hypermedia-shape MUST #/_actions/Do_It"}},
		{"action extra field", `{"_actions":{"x":{"href":"/","method":"PUT","body":1}}}`, []string{"hypermedia-shape MUST #/_actions/x/body"}},
		{"action title number", `{"_actions":{"x":{"href":"/","method":"PUT","title":3}}}`, []string{"hypermedia-shape MUST #/_actions/x/title"}},
		{"action not object", `{"_actions":{"x":[]}}`, []string{"hypermedia-shape MUST #/_actions/x"}},
		{"fields not array", `{"_actions":{"x":{"href":"/","method":"PUT","fields":{}}}}`, []string{"hypermedia-shape MUST #/_actions/x/fields"}},
		{"field not object", `{"_actions":{"x":{"href":"/","method":"PUT","fields":[1]}}}`, []string{"hypermedia-shape MUST #/_actions/x/fields/0"}},
		{"field missing name", `{"_actions":{"x":{"href":"/","method":"PUT","fields":[{"type":"string"}]}}}`, []string{"hypermedia-shape MUST #/_actions/x/fields/0"}},
		{"field bad type", `{"_actions":{"x":{"href":"/","method":"PUT","fields":[{"name":"a","type":"date"}]}}}`, []string{"hypermedia-shape MUST #/_actions/x/fields/0/type"}},
		{"field required not bool", `{"_actions":{"x":{"href":"/","method":"PUT","fields":[{"name":"a","type":"string","required":"yes"}]}}}`, []string{"hypermedia-shape MUST #/_actions/x/fields/0/required"}},
		{"field title number", `{"_actions":{"x":{"href":"/","method":"PUT","fields":[{"name":"a","type":"string","title":1}]}}}`, []string{"hypermedia-shape MUST #/_actions/x/fields/0/title"}},
		{"field extra", `{"_actions":{"x":{"href":"/","method":"PUT","fields":[{"name":"a","type":"string","min":1}]}}}`, []string{"hypermedia-shape MUST #/_actions/x/fields/0/min"}},
		{"links and actions order", `{"_links":{"a_b":{"href":"/"}},"_actions":{}}`,
			[]string{"hypermedia-shape SHOULD #/_actions", "hypermedia-shape MUST #/_links/a_b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertFindings(t, CheckHypermedia(parse(t, tc.json), "#"), tc.want)
		})
	}
}

func TestCheckProblem(t *testing.T) {
	const ok = `"type":"https://x/y","title":"T","status":404`
	tests := []struct {
		name   string
		json   string
		status int
		want   []string
	}{
		{"valid minimal", `{` + ok + `}`, 404, nil},
		{"valid full", `{` + ok + `,"detail":"d","instance":"/i","code":"AP-E1","errors":[{"detail":"d","pointer":"#/a"}]}`, 404, nil},
		{"missing type", `{"title":"T","status":404}`, 404, []string{"problem-details SHOULD #"}},
		{"type number", `{"type":1,"title":"T","status":404}`, 404, []string{"problem-details MUST #/type"}},
		{"missing title", `{"type":"x","status":404}`, 404, []string{"problem-details MUST #"}},
		{"title number", `{"type":"x","title":1,"status":404}`, 404, []string{"problem-details MUST #/title"}},
		{"missing status", `{"type":"x","title":"T"}`, 404, []string{"problem-details MUST #"}},
		{"status string", `{"type":"x","title":"T","status":"404"}`, 404, []string{"problem-details MUST #/status"}},
		{"status fraction", `{"type":"x","title":"T","status":404.5}`, 404, []string{"problem-details MUST #/status"}},
		{"status mismatch", `{` + ok + `}`, 400, []string{"problem-details MUST #/status"}},
		{"detail number", `{` + ok + `,"detail":1}`, 404, []string{"problem-details MUST #/detail"}},
		{"instance number", `{` + ok + `,"instance":1}`, 404, []string{"problem-details MUST #/instance"}},
		{"code uuid", `{` + ok + `,"code":"550e8400-e29b-41d4-a716-446655440000"}`, 404, []string{"error-code-format MUST #/code"}},
		{"code no dash", `{` + ok + `,"code":"AP"}`, 404, []string{"error-code-format MUST #/code"}},
		{"code not string", `{` + ok + `,"code":12}`, 404, []string{"error-code-format MUST #/code"}},
		{"debug", `{` + ok + `,"debug":{"stack":"x"}}`, 404, []string{"no-debug-field MUST #/debug"}},
		{"errors not array", `{` + ok + `,"errors":{}}`, 404, []string{"problem-details MUST #/errors"}},
		{"errors item not object", `{` + ok + `,"errors":[1]}`, 404, []string{"problem-details MUST #/errors/0"}},
		{"errors item missing pointer", `{` + ok + `,"errors":[{"detail":"d"}]}`, 404, []string{"problem-details MUST #/errors/0"}},
		{"errors item detail number", `{` + ok + `,"errors":[{"detail":1,"pointer":"p"}]}`, 404, []string{"problem-details MUST #/errors/0/detail"}},
		{"errors item pointer number", `{` + ok + `,"errors":[{"detail":"d","pointer":1}]}`, 404, []string{"problem-details MUST #/errors/0/pointer"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, ok := parse(t, tc.json).(map[string]any)
			if !ok {
				t.Fatal("not an object")
			}
			assertFindings(t, CheckProblem(body, tc.status, "#"), tc.want)
		})
	}
}

func TestVocabularyExported(t *testing.T) {
	if !slices.Contains(ActionMethods, "PATCH") || !slices.Contains(FieldTypes, "integer") ||
		len(LinkFields) != 2 || len(ActionFields) != 4 || len(FieldFields) != 4 ||
		len(PaginationRequired) != 4 || len(PaginationOptional) != 2 {
		t.Fatal("vocabulary mismatch")
	}
}
