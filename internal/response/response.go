// Package response checks HTTP responses against the specification (the "check" mode).
package response

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/floriankraemer/http-api-spec/validator/internal/finding"
	"github.com/floriankraemer/http-api-spec/validator/internal/jsonrules"
)

// Rule IDs reported by this package; shared rules live in package jsonrules.
const (
	RuleStatusCategory     = "status-category"
	RuleDeprecationHeaders = "deprecation-headers"
	RuleDeleteStatus       = "delete-status"
	RuleObfuscatedIDs      = "obfuscated-ids"
)

// MaxBodySize is the largest response body or input document accepted, in bytes (10 MiB).
const MaxBodySize = 10 << 20

// ErrTooLarge is returned when an input exceeds MaxBodySize.
var ErrTooLarge = errors.New("exceeds 10 MiB")

// ReadLimited reads all of r, failing with ErrTooLarge if it holds more than MaxBodySize bytes.
func ReadLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBodySize {
		return nil, ErrTooLarge
	}
	return data, nil
}

// Response is an HTTP response reduced to what the checks need.
type Response struct {
	// Method is the upper-case request method, or "" if unknown.
	Method string
	Status int
	Header http.Header
	Body   []byte
}

// normalizeStatusLine rewrites an HTTP/2 or HTTP/3 status line (as saved by `curl -i`) to HTTP/1.1,
// which net/http can parse; the rest of the response is untouched.
func normalizeStatusLine(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	line, _ := br.ReadSlice('\n') // on error the partial line is still replayed below
	proto, rest, ok := bytes.Cut(line, []byte(" "))
	if ok {
		switch string(proto) {
		case "HTTP/2", "HTTP/2.0", "HTTP/3", "HTTP/3.0":
			line = append([]byte("HTTP/1.1 "), rest...)
		}
	}
	return io.MultiReader(bytes.NewReader(slices.Clone(line)), br)
}

// Parse reads a raw HTTP/1.1, HTTP/2 or HTTP/3 response from r. method is the request method, if known.
func Parse(r io.Reader, method string) (*Response, error) {
	method = strings.ToUpper(method)
	var req *http.Request
	if method != "" {
		req = &http.Request{Method: method}
	}
	resp, err := http.ReadResponse(bufio.NewReader(normalizeStatusLine(r)), req)
	if err != nil {
		return nil, fmt.Errorf("parse HTTP response: %w", err)
	}
	return capture(resp, method)
}

// Fetch performs the request and captures the response. Redirects are followed by client's policy.
func Fetch(ctx context.Context, client *http.Client, method, url string, header http.Header) (*Response, error) {
	method = strings.ToUpper(method)
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, vs := range header {
		req.Header[k] = append([]string(nil), vs...)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", method, url, err)
	}
	return capture(resp, method)
}

func capture(resp *http.Response, method string) (*Response, error) {
	defer resp.Body.Close()
	body, err := ReadLimited(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	return &Response{Method: method, Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

var deprecationRe = regexp.MustCompile(`^@-?\d+$`)

func mk(rule string, lvl finding.Level, loc, format string, args ...any) finding.Finding {
	return finding.Finding{RuleID: rule, Level: lvl, Location: loc, Message: fmt.Sprintf(format, args...)}
}

// Check applies every response rule and returns the findings sorted with finding.Sort.
func Check(r *Response) []finding.Finding {
	var out []finding.Finding
	if r.Status < 100 || r.Status > 599 {
		out = append(out, mk(RuleStatusCategory, finding.Must, "#", "status %d is not a valid HTTP status (100-599)", r.Status))
	} else {
		out = append(out, checkDelete(r)...)
		out = append(out, checkBody(r)...)
	}
	out = append(out, checkHeaders(r.Header)...)
	finding.Sort(out)
	return out
}

func checkHeaders(h http.Header) []finding.Finding {
	var out []finding.Finding
	if v := h.Get("Deprecation"); h.Values("Deprecation") != nil && !deprecationRe.MatchString(v) {
		out = append(out, mk(RuleDeprecationHeaders, finding.Must, "#", "Deprecation header %q must be a structured-date such as @1767225600", v))
	}
	if v := h.Get("Sunset"); h.Values("Sunset") != nil {
		if _, err := http.ParseTime(v); err != nil {
			out = append(out, mk(RuleDeprecationHeaders, finding.Must, "#", "Sunset header %q must be an HTTP-date", v))
		}
	}
	return out
}

func checkDelete(r *Response) []finding.Finding {
	if r.Method != http.MethodDelete || r.Status >= 400 {
		return nil
	}
	switch r.Status {
	case 200, 202, 204:
		return nil
	}
	return []finding.Finding{mk(RuleDeleteStatus, finding.Must, "#", "DELETE must answer 200, 202 or 204, got %d", r.Status)}
}

func checkBody(r *Response) []finding.Finding {
	isSuccess := r.Status >= 200 && r.Status < 300
	isError := r.Status >= 400
	if !isSuccess && !isError {
		return nil
	}
	if len(bytes.TrimSpace(r.Body)) == 0 {
		if isError {
			return []finding.Finding{mk(RuleStatusCategory, finding.Must, "#", "error response %d must have an application/problem+json body", r.Status)}
		}
		return nil
	}

	want := "application/json"
	if isError {
		want = "application/problem+json"
	}
	var out []finding.Finding
	got, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	mediaOK := got == want
	if !mediaOK {
		out = append(out, mk(RuleStatusCategory, finding.Must, "#", "Content-Type must be %s, got %q", want, r.Header.Get("Content-Type")))
	}

	var doc any
	if err := json.Unmarshal(r.Body, &doc); err != nil {
		if mediaOK {
			out = append(out, mk(RuleStatusCategory, finding.Must, "#", "body is not valid JSON: %v", err))
		}
		return out
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return append(out, mk(RuleStatusCategory, finding.Must, "#", "body must be a JSON object"))
	}

	out = append(out, jsonrules.CheckKeys(obj, "#")...)
	out = append(out, jsonrules.CheckHypermedia(obj, "#")...)
	if isError {
		return append(out, jsonrules.CheckProblem(obj, r.Status, "#")...)
	}
	if _, ok := obj["data"].(map[string]any); !ok {
		out = append(out, mk(RuleStatusCategory, finding.Must, "#", "success body must have an object member \"data\""))
	}
	if p, ok := obj["pagination"]; ok {
		out = append(out, jsonrules.CheckPagination(p, finding.Pointer("#", "pagination"))...)
	}
	if data, ok := obj["data"]; ok {
		out = append(out, nestedPagination(data, finding.Pointer("#", "data"))...)
	}
	return append(out, numericIDs(obj, "#")...)
}

// nestedPagination reports every "pagination" member at or below v.
func nestedPagination(v any, ptr string) []finding.Finding {
	var out []finding.Finding
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			loc := finding.Pointer(ptr, k)
			if k == "pagination" {
				out = append(out, mk(jsonrules.RulePagination, finding.Must, loc, "pagination must be a top-level member"))
			}
			out = append(out, nestedPagination(val, loc)...)
		}
	case []any:
		for i, e := range x {
			out = append(out, nestedPagination(e, finding.Pointer(ptr, fmt.Sprint(i)))...)
		}
	}
	return out
}

func isIDName(k string) bool {
	return k == "id" || strings.HasSuffix(k, "Id") || strings.HasSuffix(k, "ID")
}

// numericIDs reports id members holding JSON numbers; ids must be obfuscated strings.
func numericIDs(v any, ptr string) []finding.Finding {
	var out []finding.Finding
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			loc := finding.Pointer(ptr, k)
			if _, isNum := val.(float64); isNum && isIDName(k) {
				out = append(out, mk(RuleObfuscatedIDs, finding.Should, loc, "%q is a number; identifiers should be opaque strings", k))
			}
			out = append(out, numericIDs(val, loc)...)
		}
	case []any:
		for i, e := range x {
			out = append(out, numericIDs(e, finding.Pointer(ptr, fmt.Sprint(i)))...)
		}
	}
	return out
}

// IsJSONMediaType reports whether h's Content-Type is application/json or any "+json" type.
func IsJSONMediaType(h http.Header) bool {
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}
