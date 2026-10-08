// Package har parses HAR 1.2 (HTTP Archive) files into responses that package response can check.
package har

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// countingReader counts bytes so parse can tell whether the limit was hit.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Parse reads a HAR document from r.
func Parse(r io.Reader) ([]Entry, error) { return parse(r, MaxFileSize) }

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// parse takes the size limit as a parameter so tests need not stream 256 MiB.
// It decodes log.entries one element at a time so raw body text is released after conversion.
func parse(r io.Reader, limit int64) ([]Entry, error) {
	cr := &countingReader{r: io.LimitReader(r, limit+1)}
	br := bufio.NewReader(cr)
	if b, _ := br.Peek(len(utf8BOM)); bytes.Equal(b, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	entries, err := decodeDoc(json.NewDecoder(br))
	if cr.n > limit {
		return nil, fmt.Errorf("HAR file exceeds %d MiB", MaxFileSize>>20)
	}
	if err != nil {
		return nil, fmt.Errorf("parse HAR: %w", err)
	}
	return entries, nil
}

func decodeDoc(dec *json.Decoder) ([]Entry, error) {
	var entries []Entry
	found := false
	err := eachMember(dec, func(key string) error {
		if key != "log" {
			return skipValue(dec)
		}
		return eachMember(dec, func(key string) error {
			if key != "entries" {
				return skipValue(dec)
			}
			tok, err := dec.Token()
			if err != nil {
				return err
			}
			if tok != json.Delim('[') {
				return errors.New("log.entries is not an array")
			}
			found = true
			entries = []Entry{}
			for dec.More() {
				var re rawEntry
				if err := dec.Decode(&re); err != nil {
					return err
				}
				entries = append(entries, convert(len(entries), re))
			}
			_, err = dec.Token() // closing ]
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("missing log.entries")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after top-level value")
	}
	return entries, nil
}

// eachMember consumes a JSON object, calling fn after each key; fn must consume the value.
// A non-object value (including null) is consumed and ignored.
func eachMember(dec *json.Decoder, fn func(key string) error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == json.Delim('[') {
		return skipRest(dec)
	}
	if tok != json.Delim('{') {
		return nil
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		if err := fn(k.(string)); err != nil {
			return err
		}
	}
	_, err = dec.Token() // closing }
	return err
}

// skipValue consumes one JSON value of any shape.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == json.Delim('{') || tok == json.Delim('[') {
		return skipRest(dec)
	}
	return nil
}

// skipRest consumes tokens up to the delimiter that closes the already-opened container.
func skipRest(dec *json.Decoder) error {
	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
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
	case (c.Text == nil || *c.Text == "") && c.Size > 0 && rr.Status != http.StatusNoContent && rr.Status != http.StatusNotModified:
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

// Subject names the entry for findings as "METHOD scheme://host/path".
// Userinfo, query and fragment are dropped because they routinely carry credentials.
func (e Entry) Subject() string {
	u, err := url.Parse(e.URL)
	if err != nil {
		return e.Method + " <invalid URL>"
	}
	return e.Method + " " + (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.EscapedPath()}).String()
}

// IsAPI reports whether e looks like an API call: a JSON response, a fetch/XHR request,
// or a PUT/PATCH/DELETE (browsers never use these for page assets).
func IsAPI(e Entry) bool {
	return e.Response != nil &&
		(response.IsJSONMediaType(e.Response.Header) || e.ResourceType == "fetch" || e.ResourceType == "xhr" ||
			e.Method == http.MethodPut || e.Method == http.MethodPatch || e.Method == http.MethodDelete)
}
