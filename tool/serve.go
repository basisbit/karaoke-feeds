package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// feedHandler serves a published feed folder with the headers the spec recommends. No directory listings.
func feedHandler(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if rel == "" {
			rel = "index.html"
		}
		file := filepath.Join(dir, filepath.FromSlash(rel))
		if st, err := os.Stat(file); err != nil || !st.Mode().IsRegular() {
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
		http.ServeFile(w, r, file) // handles Range and If-Modified-Since
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
