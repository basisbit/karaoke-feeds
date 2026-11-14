package main

import (
	"fmt"
	"io"
	"strings"
)

// cacheStatus returns the CDN's cache verdict for a response, or "" if no known CDN answered.
// Cloudflare sends CF-Cache-Status, bunny.net sends CDN-Cache.
func cacheStatus(s source, rel string) (cdn, status string) {
	resp, err := s.request("GET", rel, "bytes=0-99")
	if err != nil {
		return "", ""
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if v := resp.Header.Get("CF-Cache-Status"); v != "" {
		return "Cloudflare", strings.ToUpper(v)
	}
	if v := resp.Header.Get("CDN-Cache"); v != "" {
		return "bunny.net", strings.ToUpper(v)
	}
	return "", ""
}

// checkCDNCache asks twice for each file; the second answer must come from the CDN's cache.
func checkCDNCache(s source, files map[string]string, c *checker) {
	for _, what := range []string{"blobs", "feed.json"} {
		rel := files[what]
		if rel == "" {
			continue
		}
		cacheStatus(s, rel)
		cdn, status := cacheStatus(s, rel)
		switch {
		case cdn == "":
			return // no CDN in front
		case status == "HIT" || status == "UPDATING" || status == "REVALIDATED" || status == "STALE":
		case cdn == "Cloudflare" && (status == "DYNAMIC" || status == "BYPASS"):
			c.warn(fmt.Sprintf("Cloudflare does not cache %s (CF-Cache-Status: %s): every request goes to your server. Add the Cache Rule from docs/hosting.md section 5.2", what, status))
		default:
			c.warn(fmt.Sprintf("%s did not serve %s from its cache on the second request (status %s). Check the CDN's cache settings (docs/hosting.md section 5)", cdn, what, status))
		}
	}
}
