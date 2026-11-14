package main

// The landing page: kfeed build writes index.html, index.css and index.js next to feed.json, so that a person who
// opens the feed's address in a browser sees what it is and how to use it. Static, self-contained (no fonts,
// scripts or images from other sites), escaped by html/template.

import (
	_ "embed"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const repoURL = "https://github.com/basisbit/karaoke-feeds"

var songPageRe = regexp.MustCompile(`^songs-[a-z0-9][a-z0-9._-]{0,63}\.html$`)

//go:embed site/index.html.tmpl
var siteHTML string

//go:embed site/songs.html.tmpl
var songsHTML string

//go:embed site/index.css
var siteCSS string

//go:embed site/index.js
var siteJS string

var (
	siteFuncs = template.FuncMap{"num": thousands}
	siteTmpl  = template.Must(template.New("index").Funcs(siteFuncs).Parse(siteHTML))
	songsTmpl = template.Must(template.New("songs").Funcs(siteFuncs).Parse(songsHTML))
)

// thousands writes 29999 as "29,999".
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// inlineLimit: feeds up to this many songs list them on the main page; bigger feeds get one page per category,
// so that the main page stays small even with tens of thousands of songs.
const inlineLimit = 100

type siteSong struct{ Artist, Title, License string }

type siteCatalog struct {
	ID, Name, Size, Page string
	Songs                int
	List                 []siteSong
}

type siteLicense struct {
	ID, URL string
	Count   int
}

type siteData struct {
	Name, Description, Homepage, Operator, Contact, ContactText string
	URL, Key, Slug, Size, Updated, Spec, Version, Repo          string
	ConfiguredURL, Signed, Inline, Private                      bool
	Songs, Undeclared                                           int
	Sequence                                                    int64
	Keys, Mirrors                                               []string
	Catalogs                                                    []siteCatalog
	Licenses                                                    []siteLicense
}

func humanSize(b int64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(b)/1e6)
	default:
		return fmt.Sprintf("%.0f KB", float64(b)/1e3)
	}
}

// writeSite renders the page for a feed. songsByCat holds the songs of each catalog, in feed.Catalogs order.
func writeSite(out, publicURL string, private bool, feed Feed, songsByCat [][]Song, totalBytes int64) error {
	d := siteData{Name: feed.Name, Description: feed.Description, Homepage: feed.Homepage, Operator: feed.Operator.Name,
		Contact: feed.Operator.Contact, ContactText: strings.TrimPrefix(feed.Operator.Contact, "mailto:"),
		Signed: len(feed.Keys) > 0, Keys: feed.Keys, Mirrors: feed.Mirrors, Spec: Spec, Sequence: feed.Sequence,
		Version: version, Repo: repoURL, Size: humanSize(totalBytes), URL: "…/feed.json", Slug: slug(feed.Name),
		Private: private}
	if d.Slug == "" {
		d.Slug = "karaoke-feed"
	}
	if publicURL != "" {
		d.URL, d.ConfiguredURL = publicURL, true
	}
	if len(feed.Keys) > 0 {
		d.Key = strings.TrimPrefix(feed.Keys[0], "ed25519:")
	}
	if t, err := time.Parse(time.RFC3339, feed.Generated); err == nil {
		d.Updated = t.Format("2 Jan 2006")
	}
	licenses := map[string]*siteLicense{}
	for i, ref := range feed.Catalogs {
		name := ref.Names["en"]
		if name == "" {
			name = ref.ID
		}
		c := siteCatalog{ID: ref.ID, Name: name, Songs: ref.Songs, Size: humanSize(ref.Bytes), Page: "songs-" + ref.ID + ".html"}
		songs := append([]Song(nil), songsByCat[i]...)
		sort.Slice(songs, func(a, b int) bool {
			if !strings.EqualFold(songs[a].Artist, songs[b].Artist) {
				return strings.ToLower(songs[a].Artist) < strings.ToLower(songs[b].Artist)
			}
			return strings.ToLower(songs[a].Title) < strings.ToLower(songs[b].Title)
		})
		for _, s := range songs {
			d.Songs++
			lic := License{SPDX: "NOLICENSE"}
			for _, f := range s.Files {
				if f.Role == "audio" && f.License != nil {
					lic = *f.License
				}
			}
			if lic.SPDX == "NOLICENSE" {
				d.Undeclared++
			}
			if licenses[lic.SPDX] == nil {
				licenses[lic.SPDX] = &siteLicense{ID: lic.SPDX, URL: lic.URL}
			}
			licenses[lic.SPDX].Count++
			c.List = append(c.List, siteSong{Artist: s.Artist, Title: s.Title, License: lic.SPDX})
		}
		d.Catalogs = append(d.Catalogs, c)
	}
	for _, l := range licenses {
		d.Licenses = append(d.Licenses, *l)
	}
	sort.Slice(d.Licenses, func(a, b int) bool {
		if d.Licenses[a].Count != d.Licenses[b].Count {
			return d.Licenses[a].Count > d.Licenses[b].Count
		}
		return d.Licenses[a].ID < d.Licenses[b].ID
	})
	d.Inline = d.Songs <= inlineLimit
	var page strings.Builder
	if err := siteTmpl.Execute(&page, d); err != nil {
		return fmt.Errorf("landing page: %v", err)
	}
	files := map[string]string{"index.html": page.String(), "index.css": siteCSS, "index.js": siteJS}
	for _, c := range d.Catalogs {
		var sp strings.Builder
		if err := songsTmpl.Execute(&sp, map[string]any{"Feed": d.Name, "Catalog": c, "Version": d.Version, "Repo": d.Repo}); err != nil {
			return fmt.Errorf("song page: %v", err)
		}
		files[c.Page] = sp.String()
	}
	for name, content := range files {
		if err := writeAtomic(filepath.Join(out, name), []byte(content)); err != nil {
			return err
		}
	}
	// song pages of categories that no longer exist
	if entries, err := os.ReadDir(out); err == nil {
		for _, e := range entries {
			if songPageRe.MatchString(e.Name()) && files[e.Name()] == "" {
				os.Remove(filepath.Join(out, e.Name()))
			}
		}
	}
	return nil
}
