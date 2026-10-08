// Package har parses HAR 1.2 (HTTP Archive) files into responses that package response can check.
package har

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/floriankraemer/http-api-spec/validator/internal/response"
)

var weekdays = map[string]bool{"Mon": true, "Tue": true, "Wed": true, "Thu": true, "Fri": true, "Sat": true, "Sun": true}

// MaxFileSize bounds a whole HAR document.
const MaxFileSize = 256 << 20

// Entry is one log.entries element.
type Entry struct {
	Index        int                // position in log.entries
	Method, URL  string             // request method (upper-case) and URL
	ResourceType string             // Chrome "_resourceType", "" if absent
	Response     *response.Response // nil when skipped
	Skip         string             // non-empty reason when skipped
}

type nameValue struct{ Name, Value string }

type rawEntry struct {
	ResourceType string `json:"_resourceType"`
	Request      struct{ Method, URL string }
	Response     *struct {
		Status  int
		Headers []nameValue
		Content struct {
			Size     int64
			MimeType string
			Text     *string
			Encoding string
		}
	}
}

type rawHAR struct {
	Log *struct {
		Entries *[]rawEntry
	}
}

// Parse reads a HAR document from r.
func Parse(r io.Reader) ([]Entry, error) { return parse(r, MaxFileSize) }

// parse takes the size limit as a parameter so tests need not stream 256 MiB.
func parse(r io.Reader, limit int64) ([]Entry, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read HAR: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("HAR file exceeds %d MiB", MaxFileSize>>20)
	}
	var doc rawHAR
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse HAR: %w", err)
	}
	if doc.Log == nil || doc.Log.Entries == nil {
		return nil, errors.New("parse HAR: missing log.entries")
	}
	out := make([]Entry, len(*doc.Log.Entries))
	for i, re := range *doc.Log.Entries {
		out[i] = convert(i, re)
	}
	return out, nil
}

func convert(i int, re rawEntry) Entry {
	method := strings.ToUpper(re.Request.Method)
	e := Entry{Index: i, Method: method, URL: re.Request.URL, ResourceType: re.ResourceType}
	rr := re.Response
	if rr == nil || rr.Status == 0 {
		e.Skip = "no response"
		return e
	}
	c := rr.Content
	var body []byte
	switch {
	case c.Text == nil && c.Size > 0:
		e.Skip = "body not recorded"
		return e
	case c.Text != nil && c.Encoding == "base64":
		var err error
		if body, err = base64.StdEncoding.DecodeString(*c.Text); err != nil {
			e.Skip = "invalid base64 body"
			return e
		}
	case c.Text != nil:
		body = []byte(*c.Text)
	}
	if len(body) > response.MaxBodySize {
		e.Skip = "body exceeds 10 MiB"
		return e
	}
	h := http.Header{}
	hs := rr.Headers
	for j := 0; j < len(hs); j++ {
		nv := hs[j]
		if strings.HasPrefix(nv.Name, ":") {
			continue
		}
		// Some recorders (Playwright WebKit) split values at commas, cutting an
		// HTTP-date like "Thu, 31 Dec 2026 ..." in two; rejoin it.
		if weekdays[nv.Value] && j+1 < len(hs) && strings.EqualFold(hs[j+1].Name, nv.Name) {
			nv.Value += ", " + hs[j+1].Value
			j++
		}
		h.Add(nv.Name, nv.Value)
	}
	if h.Get("Content-Type") == "" && c.MimeType != "" {
		h.Set("Content-Type", c.MimeType)
	}
	e.Response = &response.Response{Method: method, Status: rr.Status, Header: h, Body: body}
	return e
}

// IsAPI reports whether e looks like an API call: a JSON response, a fetch/XHR request,
// or a PUT/PATCH/DELETE (browsers never use these for page assets).
func IsAPI(e Entry) bool {
	return e.Response != nil &&
		(response.IsJSONMediaType(e.Response.Header) || e.ResourceType == "fetch" || e.ResourceType == "xhr" ||
			e.Method == http.MethodPut || e.Method == http.MethodPatch || e.Method == http.MethodDelete)
}
