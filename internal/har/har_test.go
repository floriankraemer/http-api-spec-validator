package har

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/floriankraemer/http-api-spec/validator/internal/response"
)

func harWith(entry string) string {
	return `{"log":{"version":"1.2","entries":[` + entry + `]}}`
}

func parseOne(t *testing.T, entry string) Entry {
	t.Helper()
	es, err := Parse(strings.NewReader(harWith(entry)))
	if err != nil || len(es) != 1 {
		t.Fatalf("Parse: %v, %d entries", err, len(es))
	}
	return es[0]
}

func TestParseEntries(t *testing.T) {
	tests := []struct {
		name, entry string
		skip        string
		status      int
		ctype       string
		body        string
	}{
		{"chrome", `{"_resourceType":"fetch","request":{"method":"get","url":"https://x/a","httpVersion":"h2"},
		 "response":{"status":200,"httpVersion":"h2","headers":[{"name":":status","value":"200"},{"name":"content-type","value":"application/json"}],
		 "content":{"size":15,"mimeType":"application/json","text":"eyJkYXRhIjp7fX0=","encoding":"base64"}}}`,
			"", 200, "application/json", `{"data":{}}`},
		{"firefox mimeType only", `{"request":{"method":"GET","url":"u","httpVersion":"HTTP/2"},
		 "response":{"status":200,"headers":[],"content":{"size":2,"mimeType":"application/json","text":"{}"}}}`,
			"", 200, "application/json", "{}"},
		{"mimeType does not override", `{"request":{"method":"GET","url":"u"},
		 "response":{"status":200,"headers":[{"name":"Content-Type","value":"text/plain"}],"content":{"mimeType":"application/json","text":"x"}}}`,
			"", 200, "text/plain", "x"},
		{"status 0", `{"request":{"method":"GET","url":"u"},"response":{"status":0,"content":{"size":0}}}`, "no response", 0, "", ""},
		{"no response", `{"request":{"method":"GET","url":"u"}}`, "no response", 0, "", ""},
		{"text missing size>0", `{"request":{"method":"GET","url":"u"},"response":{"status":200,"content":{"size":5}}}`, "body not recorded", 0, "", ""},
		{"text missing size 0", `{"request":{"method":"GET","url":"u"},"response":{"status":204,"content":{"size":0}}}`, "", 204, "", ""},
		{"text empty string size>0", `{"request":{"method":"GET","url":"u"},"response":{"status":200,"content":{"size":5,"text":""}}}`, "", 200, "", ""},
		{"invalid base64", `{"request":{"method":"GET","url":"u"},"response":{"status":200,"content":{"text":"!!!","encoding":"base64"}}}`, "invalid base64 body", 0, "", ""},
		{"oversized", `{"request":{"method":"GET","url":"u"},"response":{"status":200,"content":{"text":"` + strings.Repeat("a", response.MaxBodySize+1) + `"}}}`, "body exceeds 10 MiB", 0, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := parseOne(t, tc.entry)
			if e.Skip != tc.skip {
				t.Fatalf("Skip = %q, want %q", e.Skip, tc.skip)
			}
			if tc.skip != "" {
				if e.Response != nil {
					t.Fatal("skipped entry has Response")
				}
				return
			}
			r := e.Response
			if r.Status != tc.status || r.Header.Get("Content-Type") != tc.ctype || string(r.Body) != tc.body {
				t.Fatalf("got %d %q %q", r.Status, r.Header.Get("Content-Type"), r.Body)
			}
			if r.Method != e.Method || e.Method != strings.ToUpper(e.Method) {
				t.Fatalf("method %q / %q", r.Method, e.Method)
			}
		})
	}
}

func TestParseMetadata(t *testing.T) {
	e := parseOne(t, `{"_resourceType":"xhr","request":{"method":"post","url":"https://x/a"},"response":{"status":201,"headers":[{"name":":status","value":"201"},{"name":"set-cookie","value":"a=1"},{"name":"Set-Cookie","value":"b=2"}],"content":{}}}`)
	if e.Index != 0 || e.Method != "POST" || e.URL != "https://x/a" || e.ResourceType != "xhr" {
		t.Fatalf("%+v", e)
	}
	if got := e.Response.Header.Values("Set-Cookie"); len(got) != 2 {
		t.Fatalf("Set-Cookie = %v", got)
	}
	if _, ok := e.Response.Header[":Status"]; ok || e.Response.Header.Get(":status") != "" {
		t.Fatal("pseudo-header kept")
	}
	es, _ := Parse(strings.NewReader(harWith(`{"request":{"method":"GET","url":"a"}},{"request":{"method":"GET","url":"b"}}`)))
	if es[1].Index != 1 {
		t.Fatalf("index = %d", es[1].Index)
	}
}

func TestIsAPI(t *testing.T) {
	h := func(ct string) *response.Response {
		return &response.Response{Header: http.Header{"Content-Type": {ct}}}
	}
	tests := []struct {
		name string
		e    Entry
		want bool
	}{
		{"json", Entry{Response: h("application/json; charset=utf-8")}, true},
		{"problem", Entry{Response: h("application/problem+json")}, true},
		{"vnd.api", Entry{Response: h("application/vnd.api+json")}, true},
		{"html fetch", Entry{Response: h("text/html"), ResourceType: "fetch"}, true},
		{"html xhr", Entry{Response: h("text/html"), ResourceType: "xhr"}, true},
		{"image", Entry{Response: h("image/png"), ResourceType: "image"}, false},
		{"delete no type", Entry{Method: "DELETE", Response: h("")}, true},
		{"put", Entry{Method: "PUT", Response: h("text/plain")}, true},
		{"patch", Entry{Method: "PATCH", Response: h("text/plain")}, true},
		{"post html", Entry{Method: "POST", Response: h("text/html")}, false},
		{"post json", Entry{Method: "POST", Response: h("application/json")}, true},
		{"delete skipped", Entry{Method: "DELETE", Skip: "no response"}, false},
		{"skipped", Entry{Skip: "no response", ResourceType: "fetch"}, false},
	}
	for _, tc := range tests {
		if got := IsAPI(tc.e); got != tc.want {
			t.Errorf("%s: IsAPI = %v", tc.name, got)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"not json":        "hello",
		"no log":          `{}`,
		"no entries":      `{"log":{}}`,
		"entries not arr": `{"log":{"entries":{}}}`,
	} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestParseFileTooLarge(t *testing.T) {
	const limit = 1 << 10
	r := io.MultiReader(strings.NewReader(`{"log":{"entries":[`), io.LimitReader(zeros{}, limit))
	_, err := parse(r, limit)
	if err == nil || !strings.Contains(err.Error(), "exceeds 256 MiB") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("unexpected EOF")
	}
}

func TestGoodEntryHasNoFindings(t *testing.T) {
	e := parseOne(t, `{"request":{"method":"GET","url":"u"},"response":{"status":200,"headers":[{"name":"content-type","value":"application/json"}],"content":{"text":"{\"data\":{\"id\":\"abc\"}}"}}}`)
	if f := response.Check(e.Response); len(f) != 0 {
		t.Fatalf("findings: %v", f)
	}
}

// Playwright's WebKit recorder splits header values at commas, so an HTTP-date
// arrives as "Thu" plus "31 Dec 2026 23:59:59 GMT".
func TestParseRejoinsSplitHTTPDates(t *testing.T) {
	e := parseOne(t, `{"request":{"method":"GET","url":"https://x/v1/a"},
	  "response":{"status":200,"headers":[
	    {"name":"Sunset","value":"Thu"},{"name":"Sunset","value":"31 Dec 2026 23:59:59 GMT"},
	    {"name":"Vary","value":"Accept"},{"name":"Vary","value":"Origin"}],
	    "content":{"size":0,"mimeType":"application/json"}}}`)
	if got := e.Response.Header.Values("Sunset"); len(got) != 1 || got[0] != "Thu, 31 Dec 2026 23:59:59 GMT" {
		t.Errorf("Sunset = %q", got)
	}
	if got := e.Response.Header.Values("Vary"); len(got) != 2 {
		t.Errorf("Vary = %q, want two values kept", got)
	}
}
