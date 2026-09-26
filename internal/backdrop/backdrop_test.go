package backdrop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	payload "github.com/M-U-C-K-A/claude-code/src"
)

type silent struct{ lines []string }

func (s *silent) Step(t string) { s.lines = append(s.lines, "› "+t) }
func (s *silent) OK(t string)   { s.lines = append(s.lines, "✓ "+t) }
func (s *silent) Warn(t string) { s.lines = append(s.lines, "! "+t) }
func (s *silent) Note(t string) { s.lines = append(s.lines, "  "+t) }

func testBackdrop(t *testing.T) *Backdrop {
	t.Helper()
	return &Backdrop{Dir: filepath.Join(t.TempDir(), "support"), App: filepath.Join(t.TempDir(), "Claude.app")}
}

func TestReadConfigIsForgiving(t *testing.T) {
	b := testBackdrop(t)
	os.MkdirAll(b.Dir, 0o700)
	os.WriteFile(b.path("config.json"), []byte(`{
		"enabled": false, "dim": "très sombre", "blur": 999, "imageBlur": -3,
		"mode": "sepia", "position": "50% 20%", "size": "stretch", "rotate": "on",
		"image": "", "somethingElse": 1
	}`), 0o644)
	cfg := b.ReadConfig()
	want := Defaults()
	want.Enabled = false         // valid, kept
	want.Blur = 80               // clamped
	want.ImageBlur = 0           // clamped
	want.Position = "50% 20%"    // valid, kept
	want.Rotate = "conversation" // anything but "off" rotates
	if cfg != want {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
	if b.ReadConfig().Dim != Defaults().Dim {
		t.Fatal("a wrongly typed value should keep its default")
	}
}

func TestMissingOrBrokenConfigGivesDefaults(t *testing.T) {
	b := testBackdrop(t)
	if b.ReadConfig() != Defaults() {
		t.Fatal("missing config.json should read as the defaults")
	}
	os.MkdirAll(b.Dir, 0o700)
	os.WriteFile(b.path("config.json"), []byte(`{not json`), 0o644)
	if b.ReadConfig() != Defaults() {
		t.Fatal("broken config.json should read as the defaults")
	}
}

func TestWriteConfigKeepsTheLoaderKeys(t *testing.T) {
	b := testBackdrop(t)
	if _, err := b.UpdateConfig(func(c *Config) { c.Dim = 0.6; c.ImageBlur = 12 }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(b.path("config.json"))
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	// Names the loader (src/loader.js) reads.
	for _, key := range []string{"enabled", "image", "rotate", "dim", "glass", "blur", "imageBlur", "position", "size", "mode", "autoClear"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("config.json lacks %q", key)
		}
	}
	if raw["dim"] != 0.6 || raw["imageBlur"] != 12.0 {
		t.Fatalf("values not written: %s", data)
	}
}

func TestEnsureSupportRefreshesThemeButKeepsCustomCSS(t *testing.T) {
	b := testBackdrop(t)
	if err := b.EnsureSupport(); err != nil {
		t.Fatal(err)
	}
	theme, _ := os.ReadFile(b.path("theme.css"))
	if string(theme) != payload.ThemeCSS {
		t.Fatal("theme.css not written")
	}
	os.WriteFile(b.path("custom.css"), []byte("/* mine */"), 0o644)
	os.WriteFile(b.path("theme.css"), []byte("/* old theme */"), 0o644)
	if err := b.EnsureSupport(); err != nil {
		t.Fatal(err)
	}
	if custom, _ := os.ReadFile(b.path("custom.css")); string(custom) != "/* mine */" {
		t.Fatal("custom.css must never be overwritten")
	}
	if theme, _ := os.ReadFile(b.path("theme.css")); string(theme) != payload.ThemeCSS {
		t.Fatal("an outdated theme.css must be replaced")
	}
	// Unchanged theme: not rewritten, so Claude does not reload for nothing.
	old := time.Now().Add(-time.Hour)
	os.Chtimes(b.path("theme.css"), old, old)
	b.EnsureSupport()
	if info, _ := os.Stat(b.path("theme.css")); !info.ModTime().Equal(old) {
		t.Fatal("an up-to-date theme.css was rewritten")
	}
}

func TestSetFixedImageFromFile(t *testing.T) {
	b := testBackdrop(t)
	picture := filepath.Join(t.TempDir(), "My Picture.jpg")
	os.WriteFile(picture, []byte("\xff\xd8\xff not really a jpeg"), 0o644)
	r := &silent{}
	if err := b.SetFixedImage(picture, r); err != nil {
		t.Fatal(err)
	}
	cfg := b.ReadConfig()
	if cfg.Rotating() || cfg.ImageSource != "My Picture.jpg" {
		t.Fatalf("config not updated: %+v", cfg)
	}
	if b.FixedImagePath() == "" {
		t.Fatal("fixed picture not stored")
	}
	if err := b.SetFixedImage(filepath.Join(t.TempDir(), "nope.jpg"), r); err == nil || !IsUserError(err) {
		t.Fatalf("a missing file should be a user error, got %v", err)
	}
}

func TestManifestListsOnlyDownloadedPaintings(t *testing.T) {
	b := testBackdrop(t)
	os.MkdirAll(b.path("gallery"), 0o755)
	os.WriteFile(b.PaintingFile("horatii"), []byte("jpeg"), 0o644)
	os.WriteFile(b.PaintingFile("school-of-athens"), []byte("jpeg"), 0o644)
	n, err := b.writeManifest()
	if err != nil || n != 2 {
		t.Fatalf("got %d, %v", n, err)
	}
	data, _ := os.ReadFile(b.path("gallery", "manifest.json"))
	var manifest []struct{ ID, Mode string }
	json.Unmarshal(data, &manifest)
	if len(manifest) != 2 || manifest[0].ID != "horatii" || manifest[1].Mode != "light" {
		t.Fatalf("unexpected manifest: %s", data)
	}
}

func TestGallerySources(t *testing.T) {
	// Wikimedia refuses non-standard thumbnail widths with HTTP 429.
	steps := []int{20, 40, 60, 120, 250, 330, 500, 960, 1280, 1920, 3840}
	if !slices.Contains(steps, commonsWidth) {
		t.Fatalf("%d is not a Wikimedia thumbnail step", commonsWidth)
	}
	seen := map[string]bool{}
	for _, p := range Gallery {
		if seen[p.ID] || (p.Mode != "dark" && p.Mode != "light") || len(p.Sources) == 0 {
			t.Errorf("bad gallery entry %+v", p)
		}
		seen[p.ID] = true
		for _, s := range p.Sources {
			if !strings.HasPrefix(s, "commons:") && !strings.HasPrefix(s, "met:") && !strings.HasPrefix(s, "https://") {
				t.Errorf("%s: unknown source kind %q", p.ID, s)
			}
			if strings.Contains(s, "?width=") {
				t.Errorf("%s: hardcoded thumbnail width in %q", p.ID, s)
			}
		}
	}
}

func TestParseCommons(t *testing.T) {
	thumb := `{"batchcomplete":true,"query":{"pages":[{"ns":6,"title":"File:A.jpg","imageinfo":[
		{"thumburl":"https://upload.wikimedia.org/thumb/a/a1/A.jpg/1920px-A.jpg","thumbwidth":1920,"url":"https://upload.wikimedia.org/a/a1/A.jpg"}]}]}}`
	if got, err := parseCommons([]byte(thumb)); err != nil || !strings.Contains(got, "1920px") {
		t.Fatalf("got %q, %v", got, err)
	}
	original := `{"query":{"pages":[{"title":"File:B.jpg","imageinfo":[{"url":"https://upload.wikimedia.org/b/B.jpg"}]}]}}`
	if got, err := parseCommons([]byte(original)); err != nil || got != "https://upload.wikimedia.org/b/B.jpg" {
		t.Fatalf("got %q, %v", got, err)
	}
	missing := `{"query":{"pages":[{"ns":6,"title":"File:Nope.jpg","missing":true,"known":false}]}}`
	if _, err := parseCommons([]byte(missing)); err == nil || !strings.Contains(err.Error(), "n'existe pas") {
		t.Fatalf("a missing file should say so, got %v", err)
	}
}

func TestChoiceParsing(t *testing.T) {
	for _, spec := range []string{"hasard", "Random", " rotation "} {
		if !IsRandomChoice(spec) {
			t.Errorf("%q should mean a painting per conversation", spec)
		}
	}
	for spec, id := range map[string]string{"socrate": "socrates", "Horaces": "horatii", "école": "school-of-athens", "pandemonium": "pandemonium"} {
		if p, ok := PaintingByID(spec); !ok || p.ID != id {
			t.Errorf("%q should be %s", spec, id)
		}
	}
	if _, ok := PaintingByID("~/Images/x.jpg"); ok {
		t.Error("a path is not a painting")
	}
}

func TestStatusWithoutClaude(t *testing.T) {
	b := testBackdrop(t)
	st := b.Status(false)
	if st.AppFound || st.Loader != LoaderMissing || st.Config != Defaults() {
		t.Fatalf("unexpected status %+v", st)
	}
}
