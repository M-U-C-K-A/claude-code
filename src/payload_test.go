package payload

// The loader and page scripts are injected as text; a syntax slip only shows up
// at runtime inside Claude. The theme's token remaps silently do nothing
// without !important. These tests catch both before they ship.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBuildLoader(t *testing.T) {
	l, err := BuildLoader("2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^2\.0\.0-[0-9a-f]{10}$`).MatchString(l.Tag) {
		t.Fatalf("unexpected tag %q", l.Tag)
	}
	if strings.Contains(l.Code, "__CB_") || !strings.Contains(l.Code, l.Tag) {
		t.Fatal("placeholders not filled")
	}
	// Same scripts, other release: still the same loader, no reinstall.
	if !l.SameLoader("1.1.0-"+l.Hash) || l.SameLoader("1.1.0-0000000000") || l.SameLoader("") {
		t.Fatal("SameLoader should compare the script hash")
	}
}

// Every declaration of one of Claude's tokens must be !important: the theme is
// injected in the "user" origin, where a normal declaration loses to the app's.
// Only our own variables (--ayu-*, --cb-*) may be normal.
func TestThemeTokenRemapsAreImportant(t *testing.T) {
	decl := regexp.MustCompile(`(?m)^\s*(--[\w-]+)\s*:\s*([^;]*);`)
	count := 0
	for _, m := range decl.FindAllStringSubmatch(ThemeCSS, -1) {
		name, value := m[1], m[2]
		if strings.HasPrefix(name, "--ayu-") || strings.HasPrefix(name, "--cb-") {
			continue
		}
		count++
		if !strings.HasSuffix(strings.TrimSpace(value), "!important") {
			t.Errorf("%s: %s is missing !important, Claude's own value would win", name, value)
		}
	}
	if count < 80 {
		t.Fatalf("expected the token remaps, found only %d declarations", count)
	}
}

// The gallery panel lives in the same user-origin sheet: claude.ai's Tailwind
// preflight resets padding, margins, radius and button backgrounds on every
// element, so each of the panel's declarations must be !important too.
func TestGalleryPanelRulesAreImportant(t *testing.T) {
	const marker = "4. gallery UI */"
	i := strings.Index(ThemeCSS, marker)
	if i < 0 {
		t.Fatal("gallery UI section not found in theme.css")
	}
	count := 0
	for _, block := range regexp.MustCompile(`\{([^{}]*)\}`).FindAllStringSubmatch(ThemeCSS[i:], -1) {
		for _, decl := range strings.Split(block[1], ";") {
			if decl = strings.TrimSpace(decl); decl == "" {
				continue
			}
			count++
			if !strings.HasSuffix(decl, "!important") {
				t.Errorf("gallery panel: %q is missing !important", decl)
			}
		}
	}
	if count < 100 {
		t.Fatalf("expected the panel rules, found only %d declarations", count)
	}
}

func TestThemeIsWellFormed(t *testing.T) {
	if strings.Count(ThemeCSS, "{") != strings.Count(ThemeCSS, "}") {
		t.Fatal("unbalanced braces in theme.css")
	}
	for _, v := range []string{"--cb-image", "--cb-dim", "--cb-glass", "--cb-blur", "--ayu-accent"} {
		if !strings.Contains(ThemeCSS, v) {
			t.Errorf("theme.css should reference %s", v)
		}
	}
}

// With Node around, check that the scripts parse the way they run: the loader
// as a script, the page script wrapped like the loader wraps it.
func TestScriptsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	l, err := BuildLoader("2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"loader.js": l.Code,
		"page.js":   "(async()=>{const CB={};\n" + PageScript + "\n})()",
	}
	for name, code := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse:\n%s", name, out)
		}
	}
}
