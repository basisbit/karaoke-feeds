package kfeed

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// servedPath lists what a published feed consists of (SPEC section 2, and the web page). Nothing else in the folder is
// served: no temporary or hidden files, no folder listings. The same list as in the configurations in hosting/.
var servedPath = regexp.MustCompile(`^(feed\.json|index\.(html|css|js)|songs-[a-z0-9][a-z0-9._-]{0,63}\.html|sig/[0-9a-f]{64}\.sig|catalog/[0-9a-f]{64}\.json(\.gz)?|blobs/[0-9a-f]{2}/[0-9a-f]{64})$`)

// feedHandler serves a published feed folder with the headers the spec recommends.
func feedHandler(dir string) http.Handler {
	// os.DirFS also refuses "..", "\" and drive names on its own, so nothing outside dir can be reached or probed.
	files := os.DirFS(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, "/")
		if rel == "" {
			rel = "index.html"
		}
		if !servedPath.MatchString(rel) {
			http.NotFound(w, r)
			return
		}
		if st, err := fs.Stat(files, rel); err != nil || !st.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if songPageRe.MatchString(rel) {
			w.Header().Set("Content-Security-Policy", pageCSP)
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		} else if page, ok := pageTypes[rel]; ok {
			w.Header().Set("Content-Type", page)
			w.Header().Set("Content-Security-Policy", pageCSP)
			w.Header().Set("Cache-Control", "public, max-age=300")
		} else if rel == "feed.json" {
			w.Header().Set("Cache-Control", "public, max-age=60")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			w.Header().Set("Content-Type", "application/octet-stream") // never guess, never re-encode
		}
		http.ServeFileFS(w, r, files, rel) // handles Range and If-Modified-Since
	})
}

// The landing page may load its own stylesheet and script, nothing else (same policy as hosting/*).
const pageCSP = "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

var pageTypes = map[string]string{
	"index.html": "text/html; charset=utf-8", "index.css": "text/css; charset=utf-8", "index.js": "text/javascript; charset=utf-8",
}

func cmdServe(args []string) error {
	fl := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fl.String("addr", ":8080", "address to listen on")
	pos, err := parse(fl, args)
	if err != nil || len(pos) != 1 {
		return errors.New("usage: kfeed serve <published-folder> [--addr :8080]")
	}
	if _, err := os.Stat(filepath.Join(pos[0], "feed.json")); err != nil {
		return fmt.Errorf("%s has no feed.json: point serve at the published folder", pos[0])
	}
	_, port, _ := net.SplitHostPort(*addr)
	fmt.Println("Serving", pos[0], "- subscribe on this network with:")
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ip, ok := a.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLoopback() {
				fmt.Printf("  http://%s:%s/feed.json\n", ip.IP, port)
			}
		}
	}
	fmt.Println("Stop with Ctrl+C.")
	return http.ListenAndServe(*addr, feedHandler(pos[0]))
}
