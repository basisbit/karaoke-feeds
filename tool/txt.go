package main

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// chart holds what the generator needs from an UltraStar txt file. The file's bytes are never changed.
type chart struct {
	headers  map[string]string
	voices   int
	lastBeat int
	note     string // encoding remark for the operator, "" if plain UTF-8
}

var (
	noteRe  = regexp.MustCompile(`^[:*FRG]\s+(-?\d+)\s+(\d+)`)
	voiceRe = regexp.MustCompile(`^P\s*\d+$`)
	cp1252  = [32]rune{0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
		0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178}
)

func decodeText(data []byte) (string, string) {
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return string(data[3:]), "UTF-8 with BOM"
	}
	if utf8.Valid(data) {
		return string(data), ""
	}
	var b strings.Builder
	for _, c := range data { // Windows-1252, the usual legacy encoding of UltraStar files
		switch {
		case c >= 0x80 && c <= 0x9F:
			b.WriteRune(cp1252[c-0x80])
		default:
			b.WriteRune(rune(c))
		}
	}
	return b.String(), "not UTF-8 (read as Windows-1252)"
}

// parseChart returns nil if the file is not an UltraStar chart (no #TITLE and #BPM header).
func parseChart(data []byte) *chart {
	text, note := decodeText(data)
	c := &chart{headers: map[string]string{}, note: note}
	voices := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			if k, v, ok := strings.Cut(line[1:], ":"); ok {
				k = strings.ToUpper(strings.TrimSpace(k))
				if _, seen := c.headers[k]; !seen {
					c.headers[k] = strings.TrimSpace(v)
				}
			}
		} else if voiceRe.MatchString(line) {
			voices[strings.ReplaceAll(line, " ", "")] = true
		} else if m := noteRe.FindStringSubmatch(line); m != nil {
			start, _ := strconv.Atoi(m[1])
			length, _ := strconv.Atoi(m[2])
			c.lastBeat = max(c.lastBeat, start+length)
		}
	}
	if c.headers["TITLE"] == "" || c.headers["BPM"] == "" {
		return nil
	}
	c.voices = max(1, len(voices))
	return c
}

func parseNum(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	return f, err == nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
