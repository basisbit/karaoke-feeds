package kfeed

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	Spec        = "karaoke-feed/0.2"
	maxFeedSize = 1 << 20
	maxCatalog  = 64 << 20
)

type Operator struct {
	Name    string `json:"name"`
	Contact string `json:"contact"`
}

type CatalogRef struct {
	ID     string            `json:"id"`
	Names  map[string]string `json:"names,omitempty"`
	Path   string            `json:"path"`
	SHA256 string            `json:"sha256"`
	Size   int64             `json:"size"`
	Songs  int               `json:"songs"`
	Bytes  int64             `json:"bytes"`
}

type Feed struct {
	Spec        string       `json:"spec"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Homepage    string       `json:"homepage,omitempty"`
	Operator    Operator     `json:"operator"`
	Sequence    int64        `json:"sequence"`
	Generated   string       `json:"generated"`
	Keys        []string     `json:"keys,omitempty"`
	Mirrors     []string     `json:"mirrors,omitempty"`
	MovedTo     string       `json:"moved_to,omitempty"`
	Removed     []string     `json:"removed,omitempty"`
	Catalogs    []CatalogRef `json:"catalogs"`
}

type License struct {
	SPDX        string `json:"spdx"`
	URL         string `json:"url,omitempty"`
	Attribution string `json:"attribution,omitempty"`
	Source      string `json:"source,omitempty"`
}

type File struct {
	Role    string   `json:"role"`
	Name    string   `json:"name"`
	SHA256  string   `json:"sha256"`
	Size    int64    `json:"size"`
	Type    string   `json:"type,omitempty"`
	Variant string   `json:"variant,omitempty"`
	License *License `json:"license,omitempty"`
}

type Song struct {
	ID           string            `json:"id"`
	Artist       string            `json:"artist"`
	Title        string            `json:"title"`
	Rendition    string            `json:"rendition,omitempty"`
	Language     []string          `json:"language,omitempty"`
	Year         int               `json:"year,omitempty"`
	Genre        []string          `json:"genre,omitempty"`
	Edition      string            `json:"edition,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Creator      []string          `json:"creator,omitempty"`
	Age          *int              `json:"age,omitempty"`
	Duration     float64           `json:"duration,omitempty"`
	Voices       int               `json:"voices,omitempty"`
	PreviewStart *float64          `json:"preview_start,omitempty"`
	ChartHashes  map[string]string `json:"chart_hashes,omitempty"`
	Files        []File            `json:"files"`
}

type Catalog struct {
	Spec  string            `json:"spec"`
	ID    string            `json:"id"`
	Songs []Song            `json:"songs"`
	Moved map[string]string `json:"moved,omitempty"`
}

var (
	idRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	hashRe   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hexRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	deviceRe = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)
	ccRe     = regexp.MustCompile(`^CC-(BY(?:-NC)?(?:-ND|-SA)?)-(\d\.\d)(?:-([A-Z]{2,3}))?$`)

	audioExt = []string{".mp3", ".ogg", ".opus", ".m4a", ".flac", ".wav"}
	imageExt = []string{".jpg", ".jpeg", ".png", ".webp"}
	roleExt  = map[string][]string{
		"txt": {".txt"}, "audio": audioExt, "vocals": audioExt, "instrumental": audioExt, "preview": audioExt,
		"video": {".mp4", ".webm", ".mkv", ".avi", ".mpg", ".mov"}, "cover": imageExt, "background": imageExt,
		"license": {".txt", ".md"},
	}
	mimeTypes = map[string]string{
		".txt": "text/plain", ".md": "text/markdown", ".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".opus": "audio/opus",
		".m4a": "audio/mp4", ".flac": "audio/flac", ".wav": "audio/wav", ".mp4": "video/mp4", ".webm": "video/webm",
		".mkv": "video/x-matroska", ".avi": "video/x-msvideo", ".mpg": "video/mpeg", ".mov": "video/quicktime",
		".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp",
	}
)

// nameProblem implements SPEC 5.2 rules 1-5. It returns "" if the name is acceptable for the role.
func nameProblem(name, role string) string {
	switch {
	case len(name) == 0 || len(name) > 160:
		return "must be 1 to 160 bytes long"
	case !utf8.ValidString(name):
		return "is not valid UTF-8"
	case strings.ContainsAny(name, `/\:*?"<>|`):
		return `contains one of / \ : * ? " < > |`
	case strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "contains a control character"
	case name[0] == '.' || name[0] == ' ':
		return "starts with a dot or a space"
	case name[len(name)-1] == '.' || name[len(name)-1] == ' ':
		return "ends with a dot or a space"
	case deviceRe.MatchString(strings.SplitN(name, ".", 2)[0]):
		return "is a Windows device name"
	}
	exts, ok := roleExt[role]
	if !ok {
		return "has the unknown role " + role
	}
	if ext := strings.ToLower(filepath.Ext(name)); !slices.Contains(exts, ext) {
		return fmt.Sprintf("has extension %q, which is not allowed for role %s", ext, role)
	}
	return ""
}

// hasCombiningMark reports names that are probably not in Unicode NFC (the standard library cannot normalize).
func hasCombiningMark(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= 0x0300 && r <= 0x036F })
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func gunzipLimited(data []byte, limit int64) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	zr.Multistream(false)
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, errors.New("decompressed catalog is larger than the limit")
	}
	return out, nil
}

func gzipBytes(data []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression) // zero header fields: same input gives same bytes
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

// marshal writes JSON without HTML escaping (so URLs and '&' stay readable).
func marshal(v any, indent bool) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	enc.Encode(v)
	if indent {
		return buf.Bytes()
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// decodeCatalog checks size and hash of a stored catalog file and parses it.
func decodeCatalog(ref CatalogRef, stored []byte) (*Catalog, error) {
	if int64(len(stored)) != ref.Size || "sha256:"+hashBytes(stored) != ref.SHA256 {
		return nil, errors.New("size or hash does not match feed.json")
	}
	raw := stored
	if strings.HasSuffix(ref.Path, ".gz") {
		var err error
		if raw, err = gunzipLimited(stored, maxCatalog); err != nil {
			return nil, err
		}
	}
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func checkMirror(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("%q must be an http(s) URL without query, fragment or user info", s)
	}
	return nil
}

func checkContact(s string) error {
	if (strings.HasPrefix(s, "mailto:") && strings.Contains(s, "@")) || strings.HasPrefix(s, "https://") {
		return nil
	}
	return fmt.Errorf("operator.contact %q must be a mailto: or https: URL", s)
}

// ccURL derives the license URL for Creative Commons SPDX ids, so operators only have to write the id.
func ccURL(spdx string) string {
	if spdx == "CC0-1.0" {
		return "https://creativecommons.org/publicdomain/zero/1.0/"
	}
	m := ccRe.FindStringSubmatch(spdx)
	if m == nil {
		return ""
	}
	u := "https://creativecommons.org/licenses/" + strings.ToLower(m[1]) + "/" + m[2] + "/"
	if m[3] != "" {
		u += strings.ToLower(m[3]) + "/"
	}
	return u
}

var foldTable = map[rune]string{'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'ł': "l", 'đ': "d"}

func init() {
	for base, chars := range map[string]string{
		"a": "àáâãäåāăą", "c": "çćč", "d": "ď", "e": "èéêëēėęě", "g": "ğ", "i": "ìíîïīı", "n": "ñńň", "o": "òóôõöōő",
		"r": "ř", "s": "śšş", "t": "ťţ", "u": "ùúûüūůű", "y": "ýÿ", "z": "źżž",
	} {
		for _, r := range chars {
			foldTable[r] = base
		}
	}
}

// slug turns "Künstler - Lied" into "kunstler-lied". Characters it cannot map to a-z0-9 are dropped.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		out := ""
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			out = string(r)
		case foldTable[r] != "":
			out = foldTable[r]
		case r < 128:
			dash = b.Len() > 0
			continue
		default:
			continue
		}
		if dash {
			b.WriteByte('-')
			dash = false
		}
		b.WriteString(out)
	}
	id := b.String()
	if len(id) > 56 {
		id = strings.TrimRight(id[:56], "-")
	}
	return id
}
