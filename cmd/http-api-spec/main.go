// Command http-api-spec validates OpenAPI documents and HTTP responses against the HTTP API Design Standard.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/floriankraemer/http-api-spec/validator/internal/finding"
	"github.com/floriankraemer/http-api-spec/validator/internal/har"
	"github.com/floriankraemer/http-api-spec/validator/internal/openapi"
	"github.com/floriankraemer/http-api-spec/validator/internal/response"
)

const (
	exitOK       = 0 // success, no failing findings
	exitFindings = 1 // findings failed the run
	exitUsage    = 2 // usage or I/O error
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage:
  http-api-spec lint <openapi-file> [--format text|json] [--strict]
  http-api-spec check <response-file|-> [--method M] [--format text|json] [--strict]
  http-api-spec check <recording.har|-> [--url-filter REGEX] [--all] [--no-group] [--format text|json] [--strict]
  http-api-spec check --url URL [--method GET] [--header "K: V"]... [--timeout 30s] [--format text|json] [--strict]
  http-api-spec version
  http-api-spec help

A HAR 1.2 recording (.har file, or input starting with '{') checks every API entry;
--url-filter narrows entries by URL, --all also checks non-API entries,
--no-group lists repeated findings per entry (with full URLs) instead of merging them.

Exit codes: 0 ok, 1 findings failed, 2 usage or I/O error.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch cmd := args[0]; cmd {
	case "--version", "version":
		fmt.Fprintf(stdout, "http-api-spec %s\n", version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "lint":
		return runLint(args[1:], stdin, stdout, stderr)
	case "check":
		return runCheck(args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "http-api-spec: unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

const stdinName = "<stdin>"

// headerFlag collects repeatable --header "K: V" values.
type headerFlag http.Header

func (h headerFlag) String() string { return fmt.Sprint(http.Header(h)) }

func (h headerFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, ":")
	k = strings.TrimSpace(k)
	if !ok || k == "" || textproto.TrimString(k) != k || strings.ContainsAny(k, " \t") {
		return fmt.Errorf("malformed header %q, want \"Name: value\"", v)
	}
	http.Header(h).Add(k, strings.TrimSpace(val))
	return nil
}

type commonFlags struct {
	format string
	strict bool
}

func newFlagSet(name string, stderr io.Writer, c *commonFlags) *flag.FlagSet {
	fs := flag.NewFlagSet("http-api-spec "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.format, "format", "text", "output format: text or json")
	fs.BoolVar(&c.strict, "strict", false, "fail on SHOULD findings too")
	return fs
}

// parseInterspersed parses flags that may appear before or after the positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) (positional []string, err error) {
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		rest := fs.Args()
		if consumed := args[:len(args)-len(rest)]; len(consumed) > 0 && consumed[len(consumed)-1] == "--" {
			return append(positional, rest...), nil // after the terminator everything is positional
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// parseArgs returns the positional arguments, or the exit code to stop with.
func parseArgs(fs *flag.FlagSet, args []string, c *commonFlags, stderr io.Writer) ([]string, int, bool) {
	pos, err := parseInterspersed(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return nil, exitOK, false
	case err != nil:
		return nil, exitUsage, false
	case c.format != "text" && c.format != "json":
		fmt.Fprintf(stderr, "http-api-spec: invalid --format %q, want text or json\n", c.format)
		return nil, exitUsage, false
	}
	return pos, 0, true
}

func report(stdout, stderr io.Writer, c commonFlags, source string, fs []finding.Finding) int {
	finding.Sort(fs)
	write := finding.WriteText
	if c.format == "json" {
		write = finding.WriteJSON
	}
	if err := write(stdout, source, fs); err != nil {
		fmt.Fprintf(stderr, "http-api-spec: write output: %v\n", err)
		return exitUsage
	}
	if finding.Failed(fs, c.strict) {
		return exitFindings
	}
	return exitOK
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "http-api-spec: %v\n", err)
	return exitUsage
}

// openInput opens path, or stdin for "-", and returns the name used as finding source.
func openInput(path string, stdin io.Reader) (io.ReadCloser, string, error) {
	if path == "-" {
		return io.NopCloser(stdin), stdinName, nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: opening the user-supplied file is the purpose
	return f, path, err
}

func runLint(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var c commonFlags
	fs := newFlagSet("lint", stderr, &c)
	pos, code, ok := parseArgs(fs, args, &c, stderr)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		return fail(stderr, errors.New("lint: want exactly one <openapi-file> (or -)"))
	}
	in, source, err := openInput(pos[0], stdin)
	if err != nil {
		return fail(stderr, err)
	}
	defer in.Close()
	data, err := response.ReadLimited(in)
	if err != nil {
		return fail(stderr, fmt.Errorf("%s: %w", source, err))
	}
	fns, err := openapi.Lint(data)
	if err != nil {
		return fail(stderr, fmt.Errorf("%s: %w", source, err))
	}
	return report(stdout, stderr, c, source, fns)
}

// maxRedirects is net/http's default redirect limit.
const maxRedirects = 10

// sameHostRedirects follows redirects within the original host only, so custom --header
// values such as API keys never leak to another host; the cross-host 3xx is checked as-is.
func sameHostRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if req.URL.Host != via[0].URL.Host {
		return http.ErrUseLastResponse
	}
	return nil
}

func runCheck(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var (
		c       commonFlags
		url     string
		method  string
		timeout time.Duration
		headers = headerFlag{}
		h       harFlags
	)
	fs := newFlagSet("check", stderr, &c)
	fs.StringVar(&url, "url", "", "fetch the response live from this URL")
	fs.StringVar(&method, "method", "", "request method (default GET with --url, unknown for files)")
	fs.DurationVar(&timeout, "timeout", 30*time.Second, "request timeout with --url")
	fs.Var(headers, "header", `request header "Name: value" with --url (repeatable)`)
	fs.StringVar(&h.urlFilter, "url-filter", "", "HAR: only check entries whose URL matches this regular expression")
	fs.BoolVar(&h.all, "all", false, "HAR: check every entry, not only API entries")
	fs.BoolVar(&h.noGroup, "no-group", false, "HAR: do not merge identical findings of repeated requests")
	pos, code, ok := parseArgs(fs, args, &c, stderr)
	if !ok {
		return code
	}
	if url != "" && len(pos) == 1 && strings.EqualFold(filepath.Ext(pos[0]), ".har") {
		return fail(stderr, errors.New("check: --url cannot be combined with a HAR file"))
	}
	if (url == "") == (len(pos) == 0) || len(pos) > 1 {
		return fail(stderr, errors.New("check: want exactly one of <response-file|-> or --url"))
	}

	var (
		resp   *response.Response
		source string
		err    error
	)
	if url != "" {
		if method == "" {
			method = http.MethodGet
		}
		client := &http.Client{Timeout: timeout, CheckRedirect: sameHostRedirects}
		resp, err = response.Fetch(context.Background(), client, method, url, http.Header(headers))
		source = url
	} else {
		var in io.ReadCloser
		if in, source, err = openInput(pos[0], stdin); err == nil {
			defer in.Close()
			br := bufio.NewReader(in)
			if isHAR(pos[0], br) {
				return checkHAR(br, source, h, method, c, stdout, stderr)
			}
			resp, err = response.Parse(br, method)
		}
	}
	if err != nil {
		return fail(stderr, err)
	}
	return report(stdout, stderr, c, source, response.Check(resp))
}

type harFlags struct {
	urlFilter string
	all       bool
	noGroup   bool
}

// isHAR detects a HAR by a .har extension or a leading '{' (a raw HTTP message never starts with one).
func isHAR(path string, br *bufio.Reader) bool {
	if strings.EqualFold(filepath.Ext(path), ".har") {
		return true
	}
	// Peek grows until a non-space byte shows up or the buffered window ends.
	for n := 1; ; n *= 2 {
		b, err := br.Peek(min(n, br.Size()))
		if t := strings.TrimLeft(string(b), " \t\r\n\ufeff"); t != "" {
			return t[0] == '{'
		}
		if err != nil || n >= br.Size() {
			return false
		}
	}
}

func checkHAR(in io.Reader, source string, h harFlags, method string, c commonFlags, stdout, stderr io.Writer) int {
	if method != "" {
		return fail(stderr, errors.New("check: --method cannot be combined with a HAR file; the method comes from each HAR entry"))
	}
	var filter *regexp.Regexp
	if h.urlFilter != "" {
		var err error
		if filter, err = regexp.Compile(h.urlFilter); err != nil {
			return fail(stderr, fmt.Errorf("check: invalid --url-filter: %w", err))
		}
	}
	entries, err := har.Parse(in)
	if err != nil {
		return fail(stderr, fmt.Errorf("%s: %w", source, err))
	}
	var (
		all                                        []finding.Finding
		checked, nonAPI, noBody, noResponse, other int
	)
	for _, e := range entries {
		switch {
		case e.Skip == "no response":
			noResponse++
		case e.Skip == "body not recorded":
			noBody++
		case e.Skip != "":
			other++
		case (filter != nil && !filter.MatchString(e.URL)) || (!h.all && !har.IsAPI(e)):
			nonAPI++
		default:
			checked++
			for _, f := range response.Check(e.Response) {
				f.Subject = e.Method + " " + e.URL
				all = append(all, f)
			}
		}
	}
	fmt.Fprintln(stderr, harSummary(checked, len(entries), nonAPI, noBody, noResponse, other))
	if checked == 0 {
		fmt.Fprintln(stderr, "no API entries matched; use --url-filter or --all")
		return exitUsage
	}
	if !h.noGroup {
		all = finding.Group(all)
	}
	return report(stdout, stderr, c, source, all)
}

func harSummary(checked, total, nonAPI, noBody, noResponse, other int) string {
	s := fmt.Sprintf("checked %d of %d entries", checked, total)
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{nonAPI, "non-API"}, {noBody, "without recorded body"}, {noResponse, "without response"}, {other, "other"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	if len(parts) > 0 {
		s += "; skipped " + strings.Join(parts, ", ")
	}
	return s
}
