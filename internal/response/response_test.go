package response

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/floriankraemer/http-api-spec-validator/internal/finding"
	"github.com/floriankraemer/http-api-spec-validator/internal/jsonrules"
)

func mustParse(t *testing.T, method, raw string) *Response {
	t.Helper()
	r, err := Parse(strings.NewReader(raw), method)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

func ruleIDs(fs []finding.Finding) []string {
	var ids []string
	for _, f := range fs {
		ids = append(ids, f.RuleID)
	}
	return ids
}

func TestParse(t *testing.T) {
	r := mustParse(t, "get", "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"data\":{}}")
	if r.Status != 200 || r.Method != "GET" || r.Header.Get("Content-Type") != "application/json" || string(r.Body) != `{"data":{}}` {
		t.Fatalf("unexpected response: %+v", r)
	}
	if _, err := Parse(strings.NewReader("garbage"), ""); err == nil {
		t.Fatal("expected error for malformed response")
	}
}

func TestCheck(t *testing.T) {
	const okHead = "HTTP/1.1 200 OK\nContent-Type: application/json\n"
	const probHead = "HTTP/1.1 404 Not Found\nContent-Type: application/problem+json\n"
	const prob = `{"type":"t","title":"T","status":404}`
	tests := []struct {
		name   string
		method string
		raw    string
		want   []string // expected rule IDs, sorted
	}{
		{"valid 200", "GET", okHead + "\n" + `{"data":{"user":{"id":"1"}}}`, nil},
		{"204 no body", "DELETE", "HTTP/1.1 204 No Content\n\n", nil},
		{"201 with location no body", "POST", "HTTP/1.1 201 Created\nLocation: /x\n\n", nil},
		{"wrong media type", "GET", "HTTP/1.1 200 OK\nContent-Type: text/html\n\n" + `{"data":{}}`, []string{RuleStatusCategory}},
		{"media type params ignored", "GET", "HTTP/1.1 200 OK\nContent-Type: application/json; charset=utf-8\n\n" + `{"data":{}}`, nil},
		{"missing media type", "GET", "HTTP/1.1 200 OK\n\n" + `{"data":{}}`, []string{RuleStatusCategory}},
		{"invalid json", "GET", okHead + "\n{nope", []string{RuleStatusCategory}},
		{"top level array", "GET", okHead + "\n[]", []string{RuleStatusCategory}},
		{"no data member", "GET", okHead + "\n" + `{"user":{}}`, []string{RuleStatusCategory}},
		{"data not object", "GET", okHead + "\n" + `{"data":[]}`, []string{RuleStatusCategory}},
		{"problem ok", "GET", probHead + "\n" + prob, nil},
		{"problem with json media type", "GET", "HTTP/1.1 404 Not Found\nContent-Type: application/json\n\n" + prob, []string{RuleStatusCategory}},
		{"problem invalid", "GET", probHead + "\n" + `{"type":"t","status":404}`, []string{jsonrules.RuleProblem}},
		{"problem snake extension", "GET", probHead + "\n" + `{"type":"t","title":"T","status":404,"user_id":"x"}`, []string{jsonrules.RuleCamelCase}},
		{"problem empty body", "GET", "HTTP/1.1 500 Internal Server Error\n\n", []string{RuleStatusCategory}},
		{"problem array", "GET", probHead + "\n[]", []string{RuleStatusCategory}},
		{"3xx ignored", "GET", "HTTP/1.1 302 Found\nLocation: /x\n\n", nil},
		{"status out of range", "GET", "HTTP/1.1 600 Weird\n\n", []string{RuleStatusCategory}},
		{"snake case key", "GET", okHead + "\n" + `{"data":{"a_b":1}}`, []string{jsonrules.RuleCamelCase}},
		{"bad hypermedia", "GET", okHead + "\n" + `{"data":{},"_links":{}}`, []string{jsonrules.RuleHypermedia}},
		{"pagination ok", "GET", okHead + "\n" + `{"data":{},"pagination":{"currentPage":1,"totalPages":1,"perPage":1,"totalItems":1}}`, nil},
		{"pagination bad", "GET", okHead + "\n" + `{"data":{},"pagination":{}}`, []string{jsonrules.RulePagination, jsonrules.RulePagination, jsonrules.RulePagination, jsonrules.RulePagination}},
		{"pagination nested", "GET", okHead + "\n" + `{"data":{"posts":[{"pagination":{}}]}}`, []string{jsonrules.RulePagination}},
		{"deprecation ok", "GET", okHead + "Deprecation: @-5\nSunset: Thu, 31 Dec 2026 23:59:59 GMT\n\n" + `{"data":{}}`, nil},
		{"deprecation bad", "GET", okHead + "Deprecation: true\n\n" + `{"data":{}}`, []string{RuleDeprecationHeaders}},
		{"sunset bad", "GET", okHead + "Sunset: tomorrow\n\n" + `{"data":{}}`, []string{RuleDeprecationHeaders}},
		{"sunset alone ok", "GET", okHead + "Sunset: Thu, 31 Dec 2026 23:59:59 GMT\n\n" + `{"data":{}}`, nil},
		{"delete 200 ok", "DELETE", "HTTP/1.1 200 OK\nContent-Type: application/json\n\n" + `{"data":{}}`, nil},
		{"delete 202 ok", "DELETE", "HTTP/1.1 202 Accepted\n\n", nil},
		{"delete 201", "DELETE", "HTTP/1.1 201 Created\n\n", []string{RuleDeleteStatus}},
		{"delete 404 ok", "DELETE", probHead + "\n" + prob, nil},
		{"delete 301", "DELETE", "HTTP/1.1 301 Moved Permanently\n\n", []string{RuleDeleteStatus}},
		{"get 201 not delete", "GET", "HTTP/1.1 201 Created\n\n", nil},
		{"numeric id", "GET", okHead + "\n" + `{"data":{"id":1}}`, []string{RuleObfuscatedIDs}},
		{"numeric Id suffix", "GET", okHead + "\n" + `{"data":{"items":[{"customerId":2}]}}`, []string{RuleObfuscatedIDs}},
		{"numeric ID suffix", "GET", okHead + "\n" + `{"data":{"userID":2}}`, []string{RuleObfuscatedIDs}},
		{"string id fine", "GET", okHead + "\n" + `{"data":{"id":"1","valid":3}}`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ruleIDs(Check(mustParse(t, tc.method, tc.raw)))
			want := slices.Clone(tc.want)
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("rules = %v, want %v", got, want)
			}
		})
	}
}

func TestCheckSorted(t *testing.T) {
	fs := Check(mustParse(t, "GET", "HTTP/1.1 200 OK\nContent-Type: application/json\n\n"+`{"data":{"b_x":1,"a_x":1}}`))
	if !slices.IsSortedFunc(fs, func(a, b finding.Finding) int { return strings.Compare(a.Location, b.Location) }) {
		t.Fatalf("not sorted: %+v", fs)
	}
}

func knownRules() []string {
	return []string{
		RuleStatusCategory, RuleDeprecationHeaders, RuleDeleteStatus, RuleObfuscatedIDs,
		jsonrules.RuleCamelCase, jsonrules.RuleUnderscore, jsonrules.RulePagination, jsonrules.RuleHypermedia,
		jsonrules.RuleProblem, jsonrules.RuleErrorCode, jsonrules.RuleNoDebug,
	}
}

func TestFixtures(t *testing.T) {
	files, err := filepath.Glob("../../testdata/responses/*.http")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".http")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			method := "GET"
			if strings.Contains(name, "-delete-") {
				method = "DELETE"
			}
			fs := Check(mustParse(t, method, string(raw)))
			switch {
			case strings.HasPrefix(name, "good-"):
				if len(fs) != 0 {
					t.Fatalf("expected no findings, got %+v", fs)
				}
			case strings.HasPrefix(name, "bad-"):
				rule := ""
				for _, r := range knownRules() {
					if strings.HasPrefix(strings.TrimPrefix(name, "bad-"), r) && len(r) > len(rule) {
						rule = r
					}
				}
				if rule == "" {
					t.Fatalf("no known rule prefix in fixture name %q", name)
				}
				if len(fs) == 0 {
					t.Fatalf("expected %s findings, got none", rule)
				}
				for _, f := range fs {
					if f.RuleID != rule {
						t.Errorf("unexpected finding %+v, want only %s", f, rule)
					}
				}
			default:
				t.Fatalf("fixture %q must start with good- or bad-", name)
			}
		})
	}
}

func TestFetch(t *testing.T) {
	var gotMethod, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotAccept = r.Method, r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	r, err := Fetch(context.Background(), srv.Client(), "post", srv.URL, http.Header{"Accept": {"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotAccept != "application/json" {
		t.Errorf("server saw method=%q accept=%q", gotMethod, gotAccept)
	}
	if r.Status != 201 || r.Method != "POST" || string(r.Body) != `{"data":{}}` || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("unexpected response %+v", r)
	}
	if _, err := Fetch(context.Background(), srv.Client(), "GET", "://bad", nil); err == nil {
		t.Error("expected error for bad URL")
	}
}

func TestParseHTTP2And3StatusLines(t *testing.T) {
	for _, line := range []string{"HTTP/2 200", "HTTP/2 200 ", "HTTP/2.0 200 OK", "HTTP/3 200", "HTTP/3 200 OK"} {
		r := mustParse(t, "GET", line+"\r\nContent-Type: application/json\r\nContent-Length: 11\r\n\r\n{\"data\":{}}")
		if r.Status != 200 || string(r.Body) != `{"data":{}}` || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%q: unexpected response %+v", line, r)
		}
	}
	r := mustParse(t, "GET", "HTTP/2 404 Not Found with HTTP/2 in reason\r\n\r\n")
	if r.Status != 404 {
		t.Errorf("status = %d, want 404", r.Status)
	}
}

func TestParseHeadWithoutBody(t *testing.T) {
	r := mustParse(t, "head", "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 11\r\n\r\n")
	if r.Status != 200 || r.Method != "HEAD" || len(r.Body) != 0 {
		t.Fatalf("unexpected response %+v", r)
	}
}

func TestBodyLimit(t *testing.T) {
	big := strings.Repeat("a", MaxBodySize+1)
	_, err := Parse(strings.NewReader("HTTP/1.1 200 OK\r\n\r\n"+big), "GET")
	if err == nil || !strings.Contains(err.Error(), "exceeds 10 MiB") {
		t.Errorf("Parse: err = %v, want exceeds 10 MiB", err)
	}
	ok := strings.Repeat("a", MaxBodySize)
	if r, err := Parse(strings.NewReader("HTTP/1.1 200 OK\r\n\r\n"+ok), "GET"); err != nil || len(r.Body) != MaxBodySize {
		t.Errorf("Parse at limit: err = %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.Client(), "GET", srv.URL, nil); err == nil || !strings.Contains(err.Error(), "exceeds 10 MiB") {
		t.Errorf("Fetch: err = %v, want exceeds 10 MiB", err)
	}
}

func TestMissingProblemTypeIsShould(t *testing.T) {
	r := mustParse(t, "GET", "HTTP/1.1 404 Not Found\nContent-Type: application/problem+json\n\n"+`{"title":"T","status":404}`)
	fs := Check(r)
	if len(fs) != 1 || fs[0].RuleID != jsonrules.RuleProblem || fs[0].Level != finding.Should {
		t.Fatalf("got %+v", fs)
	}
}
