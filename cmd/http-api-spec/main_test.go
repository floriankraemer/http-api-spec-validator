package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/floriankraemer/http-api-spec/validator/internal/response"
)

func runCmd(args ...string) (code int, stdout, stderr string) {
	return runIn("", args...)
}

func runIn(stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

type jsonOut struct {
	Source   string `json:"source"`
	Findings []struct {
		RuleID string `json:"ruleId"`
		Level  string `json:"level"`
	} `json:"findings"`
}

func parseJSON(t *testing.T, s string) jsonOut {
	t.Helper()
	var j jsonOut
	if err := json.Unmarshal([]byte(s), &j); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, s)
	}
	return j
}

func TestVersion(t *testing.T) {
	for _, arg := range []string{"--version", "version"} {
		code, out, errs := runCmd(arg)
		if code != exitOK || out != "http-api-spec "+version+"\n" || errs != "" {
			t.Errorf("%s: code=%d out=%q err=%q", arg, code, out, errs)
		}
	}
}

func TestHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		code, out, errs := runCmd(arg)
		if code != exitOK || errs != "" {
			t.Errorf("%s: code=%d err=%q", arg, code, errs)
		}
		for _, want := range []string{"lint <openapi-file>", "check <response-file|->"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: usage missing %q:\n%s", arg, want, out)
			}
		}
	}
}

func TestNoArgs(t *testing.T) {
	code, out, errs := runCmd()
	if code != exitUsage || out != "" || !strings.Contains(errs, "Usage:") {
		t.Errorf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, out, errs := runCmd("bogus")
	if code != exitUsage || out != "" {
		t.Errorf("code=%d out=%q", code, out)
	}
	if !strings.Contains(errs, `unknown command "bogus"`) || !strings.Contains(errs, "Usage:") {
		t.Errorf("err=%q", errs)
	}
}

const (
	openapiDir  = "../../testdata/openapi"
	responseDir = "../../testdata/responses"
)

func fixtures(t *testing.T, dir string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures in %s (err=%v)", dir, err)
	}
	return paths
}

// checkFixture runs a fixture through run and verifies exit codes against its own findings.
func checkFixture(t *testing.T, sub, path string, extra ...string) {
	t.Helper()
	base := filepath.Base(path)
	args := append([]string{sub, path}, extra...)

	code, out, errs := runCmd(append(args, "--format", "json")...)
	if errs != "" {
		t.Fatalf("stderr: %q", errs)
	}
	j := parseJSON(t, out)
	if j.Source != path {
		t.Errorf("source = %q, want %q", j.Source, path)
	}
	hasMust := false
	for _, f := range j.Findings {
		hasMust = hasMust || f.Level == "MUST"
	}
	wantCode := exitOK
	if hasMust {
		wantCode = exitFindings
	}
	if code != wantCode {
		t.Errorf("exit = %d, want %d", code, wantCode)
	}
	strictCode, _, _ := runCmd(append(args, "--strict")...)
	wantStrict := exitOK
	if len(j.Findings) > 0 {
		wantStrict = exitFindings
	}
	if strictCode != wantStrict {
		t.Errorf("--strict exit = %d, want %d", strictCode, wantStrict)
	}

	_, text, _ := runCmd(args...)
	if strings.HasPrefix(base, "good") {
		if len(j.Findings) != 0 || !strings.Contains(text, "no findings") {
			t.Errorf("good fixture has findings:\n%s", text)
		}
	} else if len(j.Findings) == 0 {
		t.Errorf("bad fixture has no findings")
	}
}

func TestLintFixtures(t *testing.T) {
	for _, p := range fixtures(t, openapiDir) {
		t.Run(filepath.Base(p), func(t *testing.T) { checkFixture(t, "lint", p) })
	}
}

func TestCheckFixtures(t *testing.T) {
	for _, p := range fixtures(t, responseDir) {
		t.Run(filepath.Base(p), func(t *testing.T) {
			var extra []string
			if strings.Contains(p, "-delete-") {
				extra = []string{"--method", "DELETE"}
			}
			checkFixture(t, "check", p, extra...)
		})
	}
}

func TestShouldOnlyFindingsPassUnlessStrict(t *testing.T) {
	p := filepath.Join(responseDir, "bad-obfuscated-ids.http")
	if code, _, _ := runCmd("check", p); code != exitOK {
		t.Errorf("non-strict exit = %d, want 0", code)
	}
	if code, _, _ := runCmd("check", p, "--strict"); code != exitFindings {
		t.Errorf("strict exit = %d, want 1", code)
	}
}

func TestJSONRuleIDs(t *testing.T) {
	code, out, _ := runCmd("lint", filepath.Join(openapiDir, "bad-version-path-prefix.yaml"), "--format", "json")
	j := parseJSON(t, out)
	if code != exitFindings || len(j.Findings) == 0 || j.Findings[0].RuleID == "" {
		t.Errorf("code=%d findings=%+v", code, j.Findings)
	}
}

func TestFlagsAfterPositional(t *testing.T) {
	p := filepath.Join(openapiDir, "good.yaml")
	for _, args := range [][]string{
		{"lint", p, "--format", "json"},
		{"lint", "--format", "json", p},
		{"lint", "--strict", p, "--format=json"},
	} {
		code, out, errs := runCmd(args...)
		if code != exitOK || errs != "" || parseJSON(t, out).Source != p {
			t.Errorf("%v: code=%d out=%q err=%q", args, code, out, errs)
		}
	}
}

func TestStdin(t *testing.T) {
	good, err := os.ReadFile(filepath.Join(openapiDir, "good.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := runIn(string(good), "lint", "-", "--format", "json")
	if code != exitOK || parseJSON(t, out).Source != "<stdin>" {
		t.Errorf("lint stdin: code=%d out=%q", code, out)
	}

	resp, err := os.ReadFile(filepath.Join(responseDir, "good-single-entity.http"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ = runIn(string(resp), "check", "-")
	if code != exitOK || !strings.HasPrefix(out, "<stdin>: no findings") {
		t.Errorf("check stdin: code=%d out=%q", code, out)
	}
}

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestCheckURL(t *testing.T) {
	var gotMethod string
	var gotHeaders http.Header
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotHeaders = r.Method, r.Header
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/bad" {
			_, _ = w.Write([]byte(`{"data":{"user":{"id":"123"}},"_meta":{"source":"cache"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"user":{"id":"123","username":"Foo"}}}`))
	})

	code, out, errs := runCmd("check", "--url", url+"/good",
		"--header", "Authorization: Bearer t", "--header", "X-Trace: 1", "--format", "json")
	if code != exitOK || errs != "" || parseJSON(t, out).Source != url+"/good" {
		t.Fatalf("good: code=%d out=%q err=%q", code, out, errs)
	}
	if gotMethod != "GET" || gotHeaders.Get("Authorization") != "Bearer t" || gotHeaders.Get("X-Trace") != "1" {
		t.Errorf("method=%q headers=%v", gotMethod, gotHeaders)
	}

	if code, out, _ := runCmd("check", "--url", url+"/bad"); code != exitFindings {
		t.Errorf("bad: code=%d out=%q", code, out)
	}

	runCmd("check", "--url", url+"/good", "--method", "post")
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
}

func TestCheckURLTimeout(t *testing.T) {
	url := serve(t, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	code, _, errs := runCmd("check", "--url", url, "--timeout", "50ms")
	if code != exitUsage || errs == "" {
		t.Errorf("code=%d err=%q", code, errs)
	}
}

func TestCheckURLUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	code, out, errs := runCmd("check", "--url", url)
	if code != exitUsage || out != "" || errs == "" {
		t.Errorf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestUsageErrors(t *testing.T) {
	good := filepath.Join(openapiDir, "good.yaml")
	resp := filepath.Join(responseDir, "good-single-entity.http")
	cases := map[string][]string{
		"lint no file":       {"lint"},
		"lint two files":     {"lint", good, good},
		"lint bad format":    {"lint", good, "--format", "xml"},
		"lint missing file":  {"lint", "does-not-exist.yaml"},
		"lint unknown flag":  {"lint", good, "--bogus"},
		"lint unparsable":    {"lint", resp},
		"check no input":     {"check"},
		"check file and url": {"check", resp, "--url", "http://127.0.0.1:1"},
		"check bad format":   {"check", resp, "--format", "xml"},
		"check bad header":   {"check", "--url", "http://127.0.0.1:1", "--header", "no-colon"},
		"check missing file": {"check", "does-not-exist.http"},
		"check unparsable":   {"check", good},
		"check bad timeout":  {"check", "--url", "http://127.0.0.1:1", "--timeout", "soon"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, out, errs := runCmd(args...)
			if code != exitUsage || out != "" || errs == "" {
				t.Errorf("code=%d out=%q err=%q", code, out, errs)
			}
		})
	}
}

func TestBinarySmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "http-api-spec")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-ldflags", "-X main.version=1.2.3", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || string(out) != "http-api-spec 1.2.3\n" {
		t.Errorf("--version: %q, %v", out, err)
	}
	out, err = exec.Command(bin, "lint", filepath.Join(openapiDir, "good.yaml")).Output()
	if err != nil || !strings.Contains(string(out), "no findings") {
		t.Errorf("lint: %q, %v", out, err)
	}
}

func TestOversizedInputs(t *testing.T) {
	big := strings.Repeat("a", response.MaxBodySize+1)
	path := filepath.Join(t.TempDir(), "big.yaml")
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, run := range map[string]func() (int, string, string){
		"lint file":  func() (int, string, string) { return runCmd("lint", path) },
		"lint stdin": func() (int, string, string) { return runIn(big, "lint", "-") },
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errs := run()
			if code != exitUsage || out != "" || !strings.Contains(errs, "exceeds 10 MiB") {
				t.Errorf("code=%d out=%q err=%q", code, out, errs)
			}
		})
	}
	code, _, errs := runIn("HTTP/1.1 200 OK\r\n\r\n"+big, "check", "-")
	if code != exitUsage || !strings.Contains(errs, "exceeds 10 MiB") {
		t.Errorf("check stdin: code=%d err=%q", code, errs)
	}
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(big)) })
	if code, _, errs := runCmd("check", "--url", url); code != exitUsage || !strings.Contains(errs, "exceeds 10 MiB") {
		t.Errorf("check url: code=%d err=%q", code, errs)
	}
}

func TestDoubleDashEndsFlags(t *testing.T) {
	good := filepath.Join(openapiDir, "good.yaml")
	if code, _, errs := runCmd("lint", "--", good, "--strict"); code != exitUsage || errs == "" {
		t.Errorf("trailing flag after --: code=%d err=%q", code, errs)
	}
	dir := t.TempDir()
	weird := filepath.Join(dir, "-weird-name.yaml")
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(weird, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if code, out, errs := runCmd("lint", "--", "-weird-name.yaml"); code != exitOK || errs != "" || !strings.HasPrefix(out, "-weird-name.yaml") {
		t.Errorf("weird name: code=%d out=%q err=%q", code, out, errs)
	}
}

func TestCheckURLRedirects(t *testing.T) {
	var otherHits int
	var otherAuth string
	other := serve(t, func(w http.ResponseWriter, r *http.Request) {
		otherHits++
		otherAuth = r.Header.Get("X-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	})
	var final bool
	var base string
	base = serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cross":
			http.Redirect(w, r, other, http.StatusFound)
		case "/same":
			http.Redirect(w, r, base+"/final", http.StatusFound)
		default:
			final = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{}}`))
		}
	})

	code, out, _ := runCmd("check", "--url", base+"/cross", "--header", "X-Api-Key: secret")
	if otherHits != 0 || otherAuth != "" {
		t.Errorf("cross-host redirect was followed (hits=%d, key=%q)", otherHits, otherAuth)
	}
	if code != exitOK && code != exitFindings || out == "" {
		t.Errorf("cross-host: code=%d out=%q", code, out)
	}

	if code, out, errs := runCmd("check", "--url", base+"/same"); code != exitOK || !final {
		t.Errorf("same-host: code=%d final=%v out=%q err=%q", code, final, out, errs)
	}
}

const harDir = "../../testdata/har"

func harFixtures(t *testing.T) []string {
	t.Helper()
	return fixtures(t, harDir)
}

func TestCheckHARFixtures(t *testing.T) {
	for _, p := range harFixtures(t) {
		base := filepath.Base(p)
		if !strings.HasSuffix(base, ".har") || base == "empty-no-api.har" {
			continue
		}
		t.Run(base, func(t *testing.T) {
			code, out, _ := runCmd("check", p, "--format", "json")
			j := parseJSON(t, out)
			hasMust := false
			for _, f := range j.Findings {
				hasMust = hasMust || f.Level == "MUST"
			}
			switch {
			case strings.HasPrefix(base, "good-"):
				if code != exitOK || len(j.Findings) != 0 {
					t.Errorf("code=%d findings=%+v", code, j.Findings)
				}
				if _, text, _ := runCmd("check", p); !strings.Contains(text, "no findings") {
					t.Errorf("text: %s", text)
				}
			case strings.HasPrefix(base, "bad-"):
				rest := strings.TrimPrefix(base, "bad-")
				if len(j.Findings) == 0 {
					t.Fatal("no findings")
				}
				for _, f := range j.Findings {
					if !strings.HasPrefix(rest, f.RuleID+"-") {
						t.Errorf("unexpected rule %q in %s", f.RuleID, base)
					}
				}
				want := exitOK
				if hasMust {
					want = exitFindings
				}
				if code != want {
					t.Errorf("exit = %d, want %d", code, want)
				}
				if c, _, _ := runCmd("check", p, "--strict"); c != exitFindings {
					t.Errorf("--strict exit = %d, want 1", c)
				}
			default:
				t.Fatalf("fixture %s has no good-/bad- prefix", base)
			}
		})
	}
}

func TestCheckHAREmptyNoAPI(t *testing.T) {
	code, _, errs := runCmd("check", filepath.Join(harDir, "empty-no-api.har"))
	if code != exitUsage || !strings.Contains(errs, "no API entries matched; use --url-filter or --all") {
		t.Errorf("code=%d stderr=%q", code, errs)
	}
}

func TestCheckHARGrouping(t *testing.T) {
	p := filepath.Join(harDir, "bad-naming-camel-case-chromium.har")
	_, out, _ := runCmd("check", p)
	if !strings.Contains(out, "(3 entries)") || strings.Contains(out, "?") {
		t.Errorf("grouped output:\n%s", out)
	}
	_, ungrouped, _ := runCmd("check", p, "--no-group")
	if strings.Contains(ungrouped, "entries)") {
		t.Errorf("--no-group output has counts:\n%s", ungrouped)
	}
	if n, g := strings.Count(ungrouped, "camel"), strings.Count(out, "camel"); n < g+2 {
		t.Errorf("ungrouped mentions %d, grouped %d", n, g)
	}
	_, js, _ := runCmd("check", p, "--format", "json")
	if !strings.Contains(js, `"subject"`) || !strings.Contains(js, `"occurrences": 3`) {
		t.Errorf("json:\n%s", js)
	}
}

func TestCheckHARURLFilter(t *testing.T) {
	bad := filepath.Join(harDir, "bad-naming-camel-case-chromium.har")
	if code, _, errs := runCmd("check", bad, "--url-filter", "^$"); code != exitUsage || !strings.Contains(errs, "no API entries matched") {
		t.Errorf("no match: code=%d err=%q", code, errs)
	}
	if code, _, errs := runCmd("check", bad, "--url-filter", "("); code != exitUsage || errs == "" {
		t.Errorf("invalid regex: code=%d err=%q", code, errs)
	}
	if code, _, _ := runCmd("check", bad, "--url-filter", "."); code != exitFindings {
		t.Errorf("match all: code=%d", code)
	}
}

func TestCheckHARStdin(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(harDir, "good-chromium.har"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, errs := runIn("  \n"+string(data), "check", "-")
	if code != exitOK || !strings.HasPrefix(out, "<stdin>: no findings") || !strings.Contains(errs, "checked ") {
		t.Errorf("code=%d out=%q err=%q", code, out, errs)
	}
}

func TestCheckHARAll(t *testing.T) {
	p := filepath.Join(harDir, "good-chromium.har")
	code, out, _ := runCmd("check", p, "--all", "--format", "json")
	if code != exitFindings || len(parseJSON(t, out).Findings) == 0 {
		t.Errorf("--all: code=%d out=%s", code, out)
	}
}

func TestCheckHARRejectsURLAndMethod(t *testing.T) {
	p := filepath.Join(harDir, "good-chromium.har")
	for _, extra := range [][]string{{"--url", "http://x"}, {"--method", "GET"}} {
		if code, _, errs := runCmd(append([]string{"check", p}, extra...)...); code != exitUsage || errs == "" {
			t.Errorf("%v: code=%d err=%q", extra, code, errs)
		}
	}
}

func TestCheckHARSummary(t *testing.T) {
	_, _, errs := runCmd("check", filepath.Join(harDir, "good-chrome-devtools.har"))
	if !strings.HasPrefix(errs, "checked ") || !strings.Contains(errs, " of ") ||
		!strings.Contains(errs, "without recorded body") || !strings.Contains(errs, "without response") {
		t.Errorf("stderr: %q", errs)
	}
	t.Log(errs)
}
