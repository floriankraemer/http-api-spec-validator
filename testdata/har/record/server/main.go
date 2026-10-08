// Recorder fixture server: serves a page plus /v1/<name> from testdata/responses/<name>.http.
package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const page = `<!doctype html><html><head><meta charset="utf-8"><title>loading</title>
<link rel="icon" href="/logo.png"></head><body><h1>Recorder</h1><img src="/logo.png" alt="logo">
<script src="/app.js"></script></body></html>`

const js = `(async () => {
  const calls = (new URLSearchParams(location.search).get('calls') || '').split(',').filter(Boolean);
  for (const c of calls) {
    const i = c.indexOf(':');
    const r = await fetch('/v1/' + c.slice(i + 1), { method: c.slice(0, i) });
    await r.text();
  }
  document.title = 'done';
})();`

// 1x1 transparent PNG.
const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

var nameRe = regexp.MustCompile(`^[a-z0-9-]+$`)

func main() {
	dir := os.Args[1] // directory with *.http files
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		fmt.Fprint(w, js)
	})
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		b, _ := base64.StdEncoding.DecodeString(pngB64)
		w.Header().Set("Content-Type", "image/png")
		w.Write(b)
	})
	mux.HandleFunc("/v1/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !nameRe.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		raw, err := os.ReadFile(filepath.Join(dir, name+".http"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
		head, body, _ := strings.Cut(string(raw), "\n\n")
		lines := strings.Split(head, "\n")
		f := strings.Fields(lines[0]) // HTTP/1.1 204 No Content
		code, err := strconv.Atoi(f[1])
		if err != nil {
			http.Error(w, "bad status line", 500)
			return
		}
		for _, l := range lines[1:] {
			if k, v, ok := strings.Cut(l, ":"); ok {
				w.Header().Add(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
		w.WriteHeader(code)
		if code != 204 && code != 304 {
			fmt.Fprint(w, body)
		}
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", mux))
}
