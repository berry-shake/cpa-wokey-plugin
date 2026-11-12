package flags

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestSVG(t *testing.T) {
	for _, code := range []string{"HK", "tw", " jp ", "US", "SG"} {
		raw, ok := SVG(code)
		if !ok || !strings.HasPrefix(string(raw), "<svg") {
			t.Fatalf("SVG(%q) = %d bytes, %v", code, len(raw), ok)
		}
	}
	for _, code := range []string{"", "H", "HKG", "../flags", "h1", "zz", "Hong Kong"} {
		if _, ok := SVG(code); ok {
			t.Fatalf("SVG(%q) found a flag", code)
		}
	}
}

// The flags are served from the CPA origin, so they must stay plain
// drawings: no scripts, event handlers, links or embedded content.
func TestFlagsAreInert(t *testing.T) {
	allowed := map[string]bool{"svg": true, "path": true, "circle": true, "ellipse": true, "g": true, "rect": true, "polygon": true}
	tag := regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9:-]*)`)
	risky := regexp.MustCompile(`(?i)\son[a-z]+\s*=|href|javascript:|url\(|@import|<!`)
	count := 0
	errWalk := fs.WalkDir(files, "svg", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		count++
		raw, _ := files.ReadFile(path)
		for _, m := range tag.FindAllStringSubmatch(string(raw), -1) {
			if !allowed[m[1]] {
				t.Errorf("%s uses <%s>", path, m[1])
			}
		}
		if loc := risky.FindString(string(raw)); loc != "" {
			t.Errorf("%s contains %q", path, loc)
		}
		return nil
	})
	if errWalk != nil {
		t.Fatal(errWalk)
	}
	if count != 258 {
		t.Fatalf("%d flags, want 258", count)
	}
}
