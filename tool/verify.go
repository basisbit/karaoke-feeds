package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// source is a published feed, either a local folder (also given as file://) or an http(s) location.
type source struct {
	base   string
	remote bool
	client *http.Client
}

// retryBase is the first wait after a 429/503 without Retry-After (SPEC 5.1.8); tests make it shorter.
var retryBase = 10 * time.Second

const maxRetries = 4

func newSource(arg string) source {
	arg = strings.TrimSuffix(arg, "feed.json")
	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 60 * time.Second
		c := &http.Client{Transport: t, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || (via[0].URL.Scheme == "https" && req.URL.Scheme != "https") {
				return errors.New("too many redirects, or redirect from https to http")
			}
			req.Header.Del("Referer") // the address we come from may be personal (SPEC 5.1)
			return nil
		}}
		return source{base: strings.TrimSuffix(arg, "/") + "/", remote: true, client: c}
	}
	if u, err := url.Parse(arg); err == nil && u.Scheme == "file" {
		arg = u.Path
		if len(arg) > 2 && arg[0] == '/' && arg[2] == ':' { // file:///C:/feeds/x on Windows
			arg = arg[1:]
		}
		arg = filepath.FromSlash(arg)
	}
	return source{base: arg}
}

// showLoc is a location as it may appear in messages and logs. A location can be personal: a private feed has an
// access token in its path (SPEC 5.5). So of a web address only the host is shown.
func showLoc(loc string) string {
	if !strings.HasPrefix(loc, "http://") && !strings.HasPrefix(loc, "https://") {
		return loc
	}
	u, err := url.Parse(loc)
	if err != nil {
		return "the feed address"
	}
	if strings.Trim(u.Path, "/") == "" {
		return u.Scheme + "://" + u.Host + "/"
	}
	return u.Scheme + "://" + u.Host + "/…/"
}

// idleTimeout aborts a download that delivers no data for this long (a stalled connection).
var idleTimeout = 60 * time.Second

type idleReader struct {
	r     io.ReadCloser
	timer *time.Timer
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	i.timer.Reset(idleTimeout)
	return n, err
}

func (i *idleReader) Close() error {
	i.timer.Stop()
	return i.r.Close()
}

func (s source) request(method, rel, rng string) (*http.Response, error) {
	return s.requestCtx(context.Background(), method, rel, rng)
}

func (s source) requestCtx(ctx context.Context, method, rel, rng string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.base+rel, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kfeed")
	req.Header.Set("Accept-Encoding", "identity") // we want the stored bytes, exactly
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := s.client.Do(req)
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err // without the address, which may be personal (showLoc)
	}
	return resp, err
}

// open starts reading a file. Remote sources wait and retry on 429 and 503 (Retry-After, else 10 s doubling).
func (s source) open(rel string) (io.ReadCloser, http.Header, error) {
	if !s.remote {
		f, err := os.Open(filepath.Join(s.base, filepath.FromSlash(rel)))
		return f, nil, err
	}
	wait := retryBase
	for try := 0; ; try++ {
		ctx, cancel := context.WithCancel(context.Background())
		resp, err := s.requestCtx(ctx, "GET", rel, "")
		if err != nil {
			cancel()
			return nil, nil, err
		}
		if resp.StatusCode == 200 {
			return &idleReader{r: resp.Body, timer: time.AfterFunc(idleTimeout, cancel)}, resp.Header, nil
		}
		resp.Body.Close()
		cancel()
		if (resp.StatusCode != 429 && resp.StatusCode != 503) || try == maxRetries {
			return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		d := wait
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs >= 0 && secs <= 600 {
			d = time.Duration(secs) * time.Second
		}
		time.Sleep(d)
		wait *= 2
	}
}

// get returns at most limit bytes of a file; a longer file is an error.
func (s source) get(rel string, limit int64) ([]byte, http.Header, error) {
	r, hdr, err := s.open(rel)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("larger than the expected %d bytes", limit)
	}
	return data, hdr, err
}

func (s source) size(rel string) (int64, error) {
	if !s.remote {
		st, err := os.Stat(filepath.Join(s.base, filepath.FromSlash(rel)))
		if err != nil {
			return 0, err
		}
		return st.Size(), nil
	}
	resp, err := s.request("HEAD", rel, "")
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.ContentLength, nil
}

type checker struct {
	errs, warns []string
}

func (c *checker) err(format string, a ...any)  { c.errs = append(c.errs, fmt.Sprintf(format, a...)) }
func (c *checker) warn(format string, a ...any) { c.warns = append(c.warns, fmt.Sprintf(format, a...)) }

func cmdVerify(args []string) error {
	fl := flag.NewFlagSet("verify", flag.ContinueOnError)
	key := fl.String("key", "", "public key (ed25519:...) or a file containing it, that must have signed the feed")
	full := fl.Bool("full", false, "download and hash every file")
	pos, err := parse(fl, args)
	if err != nil || len(pos) != 1 {
		return errors.New("usage: kfeed verify <folder-or-URL> [--key ed25519:...] [--full]")
	}
	if *key != "" {
		k, err := publicKeyArg(*key)
		if err != nil {
			return err
		}
		*key = k
	}
	c := &checker{}
	summary := verify(newSource(pos[0]), *key, *full, c)
	for _, w := range c.warns {
		fmt.Println("warning:", w)
	}
	for _, e := range c.errs {
		fmt.Println("ERROR:  ", e)
	}
	if len(c.errs) > 0 {
		return fmt.Errorf("%d problems found, the feed is NOT ok", len(c.errs))
	}
	fmt.Println("OK:", summary)
	return nil
}

func verify(s source, key string, full bool, c *checker) string {
	raw, hdr, err := s.get("feed.json", maxFeedSize)
	if err != nil {
		c.err("feed.json: %v", err)
		return ""
	}
	var feed Feed
	if err := json.Unmarshal(raw, &feed); err != nil {
		c.err("feed.json is not valid JSON: %v", err)
		return ""
	}
	if feed.Spec != Spec {
		c.err("feed.json: spec is %q, this tool checks %q", feed.Spec, Spec)
		return ""
	}
	if feed.Name == "" || feed.Operator.Name == "" {
		c.err("feed.json: name and operator.name are required")
	}
	if err := checkContact(feed.Operator.Contact); err != nil {
		c.err("feed.json: %v", err)
	}
	if feed.Sequence <= 0 {
		c.err("feed.json: sequence must be a positive integer")
	}
	if _, err := time.Parse(time.RFC3339, feed.Generated); err != nil {
		c.err("feed.json: generated must be an RFC 3339 timestamp")
	}
	for _, m := range feed.Mirrors {
		if err := checkMirror(m); err != nil {
			c.err("feed.json: mirror %v", err)
		}
	}
	for _, id := range feed.Removed {
		if !idRe.MatchString(id) {
			c.err("feed.json: removed id %q is not valid", id)
		}
	}
	if len(feed.Catalogs) == 0 {
		c.err("feed.json: no catalogs")
	}

	// signature
	signed := "unsigned"
	if len(feed.Keys) > 8 {
		c.err("feed.json: more than 8 keys")
	}
	for _, k := range feed.Keys {
		if _, err := parsePub(k); err != nil {
			c.err("feed.json: %v", err)
		}
	}
	trust := feed.Keys
	if key != "" {
		trust = []string{key}
		if !slices.Contains(feed.Keys, key) {
			c.warn("the key given with --key is not in the feed's keys: subscribers pinned to it will be locked out after this feed")
		}
	}
	if len(trust) > 0 {
		sig, _, err := s.get("sig/"+hashBytes(raw)+".sig", 1024)
		switch {
		case err != nil:
			c.err("signature sig/%s.sig: %v (was feed.json changed after signing, or the sig folder not uploaded?)", hashBytes(raw), err)
		case !sigValid(trust, raw, string(sig)):
			c.err("the signature is not valid for the expected key(s)")
		default:
			signed = fmt.Sprintf("signed, %d key(s) listed", len(feed.Keys))
		}
	} else {
		c.warn("the feed is unsigned: fine for a private feed, not allowed as a default feed in games")
	}

	// catalogs and songs
	ids := map[string]string{}
	blobs := map[string]int64{}
	var order []string
	songs, undeclared, firstAudio := 0, 0, ""
	for _, ref := range feed.Catalogs {
		name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(ref.Path, "catalog/"), ".gz"), ".json")
		if !idRe.MatchString(ref.ID) || !hashRe.MatchString(ref.SHA256) || name != strings.TrimPrefix(ref.SHA256, "sha256:") || !strings.Contains(ref.Path, ".json") {
			c.err("catalog %q: id, path or sha256 is malformed (path must be catalog/<sha256>.json or .json.gz)", ref.ID)
			continue
		}
		stored, chdr, err := s.get(ref.Path, ref.Size)
		if err != nil {
			c.err("catalog %s (%s): %v", ref.ID, ref.Path, err)
			continue
		}
		cat, err := decodeCatalog(ref, stored)
		if err != nil {
			hint := ""
			if chdr.Get("Content-Encoding") != "" {
				hint = " (the server sends Content-Encoding for this file: serve .json.gz files as they are, see docs/hosting.md)"
			}
			c.err("catalog %s: %v%s", ref.ID, err, hint)
			continue
		}
		if cat.Spec != Spec || cat.ID != ref.ID {
			c.err("catalog %s: spec or id inside the file does not match feed.json", ref.ID)
		}
		if len(cat.Songs) != ref.Songs {
			c.warn("catalog %s: feed.json says %d songs, the catalog has %d", ref.ID, ref.Songs, len(cat.Songs))
		}
		var total int64
		seen := map[string]bool{}
		for _, song := range cat.Songs {
			songs++
			if !idRe.MatchString(song.ID) {
				c.err("catalog %s: song id %q is not valid", ref.ID, song.ID)
				continue
			}
			if other, dup := ids[song.ID]; dup {
				c.err("song %s is in two catalogs (%s and %s)", song.ID, other, ref.ID)
			}
			ids[song.ID] = ref.ID
			if slices.Contains(feed.Removed, song.ID) {
				c.err("song %s is listed and also in removed", song.ID)
			}
			if song.Artist == "" || song.Title == "" {
				c.err("song %s: artist and title are required", song.ID)
			}
			txts, names, noLicense := 0, map[string]bool{}, false
			for _, f := range song.Files {
				if _, known := roleExt[f.Role]; !known {
					c.warn("song %s: unknown role %q (clients ignore the file)", song.ID, f.Role)
					continue
				}
				if p := nameProblem(f.Name, f.Role); p != "" {
					c.err("song %s: file name %q %s (clients skip the whole song)", song.ID, f.Name, p)
				}
				if names[strings.ToLower(f.Name)] {
					c.err("song %s: two files are named %q", song.ID, f.Name)
				}
				names[strings.ToLower(f.Name)] = true
				if !hashRe.MatchString(f.SHA256) || f.Size < 0 {
					c.err("song %s: file %q has a malformed sha256 or size", song.ID, f.Name)
					continue
				}
				if f.Role == "txt" {
					txts++
				}
				if f.Role != "license" {
					switch {
					case f.License == nil || f.License.SPDX == "":
						c.err("song %s: file %q has no license object", song.ID, f.Name)
					case f.License.SPDX == "NOLICENSE":
						noLicense = true
					case f.License.URL == "":
						c.err("song %s: license %s of %q has no url", song.ID, f.License.SPDX, f.Name)
					}
				}
				sum := strings.TrimPrefix(f.SHA256, "sha256:")
				if old, ok := blobs[sum]; ok && old != f.Size {
					c.err("blob %s is listed with two different sizes", sum)
				} else if !ok {
					order = append(order, sum)
				}
				blobs[sum] = f.Size
				if !seen[sum] {
					seen[sum] = true
					total += f.Size
				}
				if f.Role == "audio" && firstAudio == "" && f.Size > 1000 {
					firstAudio = sum
				}
			}
			if txts != 1 {
				c.err("song %s: needs exactly one file with role txt, has %d", song.ID, txts)
			}
			if noLicense {
				undeclared++
			}
		}
		if total != ref.Bytes {
			c.warn("catalog %s: feed.json says %d bytes, the files add up to %d", ref.ID, ref.Bytes, total)
		}
	}
	if undeclared > 0 {
		c.warn("%d songs have files without a declared license (NOLICENSE): clients hide them by default, and they are not allowed in a default feed", undeclared)
	}

	// blobs
	check := order
	if s.remote && !full && len(check) > 25 {
		check = check[:25]
	}
	for _, sum := range check {
		rel := "blobs/" + sum[:2] + "/" + sum
		if full {
			var bad []string
			ok := false
			for try := 1; try <= attempts && !ok; try++ {
				data, _, err := s.get(rel, blobs[sum])
				switch {
				case err != nil && len(data) == 0:
					bad = append(bad, err.Error())
					try = attempts // missing or refused: repeating won't help
				case int64(len(data)) != blobs[sum]:
					bad = append(bad, fmt.Sprintf("got %d of %d bytes", len(data), blobs[sum]))
				case hashBytes(data) != sum:
					bad = append(bad, "content does not match its hash")
				default:
					ok = true
				}
			}
			switch {
			case ok && len(bad) > 0:
				c.warn("blob %s: correct only on attempt %d (before: %s). The server or CDN cuts off or damages downloads now and then", sum, len(bad)+1, strings.Join(bad, "; "))
			case !ok:
				c.err("blob %s: %s", sum, strings.Join(bad, "; "))
			}
		} else if n, err := s.size(rel); err != nil {
			c.err("blob %s: %v", sum, err)
		} else if n != blobs[sum] {
			c.err("blob %s: is %d bytes, the catalog says %d", sum, n, blobs[sum])
		}
	}
	how := "sizes checked"
	if full {
		how = "fully hashed"
	}

	// web server set-up
	if s.remote {
		if hdr.Get("Access-Control-Allow-Origin") != "*" {
			c.warn("feed.json is served without 'Access-Control-Allow-Origin: *': browser-based games cannot read this feed")
		}
		if cc := hdr.Get("Cache-Control"); cc == "" {
			c.warn("feed.json has no Cache-Control header: set a short one (max-age=300) so updates arrive quickly")
		}
		if strings.HasPrefix(s.base, "http://") {
			c.warn("the feed is served over plain http: fine on a local network for a signed feed, not for a public feed")
		}
		if firstAudio != "" {
			rel := "blobs/" + firstAudio[:2] + "/" + firstAudio
			if resp, err := s.request("GET", rel, "bytes=0-99"); err == nil {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
				resp.Body.Close()
				if resp.StatusCode != 206 || len(body) != 100 {
					c.warn("the server ignores range requests: song previews will download whole files")
				}
				if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "max-age") {
					c.warn("blobs are served without a long Cache-Control (max-age=31536000, immutable): CDNs and clients will re-check them needlessly")
				}
				if resp.Header.Get("Content-Encoding") != "" {
					c.err("the server sends Content-Encoding for blobs: clients would get different bytes than the hash covers")
				}
			}
			checkCDNCache(s, map[string]string{"blobs": rel, "feed.json": "feed.json"}, c)
		}
	}
	return fmt.Sprintf("%q, sequence %d, %s, %d songs in %d catalogs, %d of %d files %s", feed.Name, feed.Sequence, signed, songs, len(feed.Catalogs), len(check), len(order), how)
}
