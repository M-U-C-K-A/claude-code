package backdrop

import (
	"encoding/json"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"regexp"
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
		"brightness": 9, "terminalGlass": 3, "mode": "sepia", "position": "50% 20%",
		"size": "stretch", "rotate": "on", "image": "", "somethingElse": 1
	}`), 0o644)
	cfg := b.ReadConfig()
	want := Defaults()
	want.Enabled = false         // valid, kept
	want.Blur = 80               // clamped
	want.ImageBlur = 0           // clamped
	want.Brightness = 1.6        // clamped
	want.TerminalGlass = 1       // clamped
	want.Position = "50% 20%"    // valid, kept
	want.Rotate = "conversation" // anything but "off" rotates
	if cfg != want {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
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
	os.MkdirAll(b.Dir, 0o700)
	// A key this version does not know, as a newer loader could write.
	os.WriteFile(b.path("config.json"), []byte(`{"dim": 0.4, "futureKnob": {"a": 1}}`), 0o644)
	if _, err := b.UpdateConfig(func(c *Config) { c.Brightness = 1.2; c.ImageBlur = 12 }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(b.path("config.json"))
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	// The names src/loader.js reads.
	for _, key := range []string{"enabled", "image", "rotate", "dim", "brightness", "imageOpacity", "imageBlur",
		"glass", "blur", "terminalGlass", "position", "size", "mode", "autoClear"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("config.json lacks %q", key)
		}
	}
	if raw["dim"] != 0.4 || raw["brightness"] != 1.2 || raw["imageBlur"] != 12.0 {
		t.Fatalf("values not written: %s", data)
	}
	if _, ok := raw["futureKnob"]; !ok {
		t.Fatalf("an unknown key was dropped: %s", data)
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

func writeManifest(t *testing.T, b *Backdrop, entries string) {
	t.Helper()
	os.MkdirAll(b.path("gallery"), 0o755)
	if err := os.WriteFile(b.path("gallery", "manifest.json"), []byte(entries), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChooseImageAddsYourFileToTheGallery(t *testing.T) {
	b := testBackdrop(t)
	picture := filepath.Join(t.TempDir(), "My Picture.jpg")
	os.WriteFile(picture, []byte("\xff\xd8\xff not really a jpeg"), 0o644)
	r := &silent{}
	if err := b.ChooseImage(picture, r); err != nil {
		t.Fatal(err)
	}
	images := b.ReadManifest()
	if len(images) != 1 || !images[0].Custom || images[0].Title != "My Picture" {
		t.Fatalf("unexpected gallery %+v", images)
	}
	if !regexp.MustCompile(`^custom-my-picture-[0-9a-z]+$`).MatchString(images[0].ID) {
		t.Fatalf("the loader only accepts custom-<slug> ids, got %q", images[0].ID)
	}
	cfg := b.ReadConfig()
	if cfg.Rotating() || cfg.Image != "gallery/"+images[0].File || b.FixedID(cfg) != images[0].ID {
		t.Fatalf("not set as the fixed picture: %+v", cfg)
	}
	if got := b.PictureSummary(cfg); got != "fixe — My Picture" {
		t.Fatalf("summary %q", got)
	}
	if err := b.ChooseImage(filepath.Join(t.TempDir(), "nope.jpg"), r); err == nil || !IsUserError(err) {
		t.Fatalf("a missing file should be a user error, got %v", err)
	}
}

func TestManifestKeepsTheImagesAddedInClaude(t *testing.T) {
	b := testBackdrop(t)
	os.MkdirAll(b.path("gallery"), 0o755)
	for _, name := range []string{"horatii.jpg", "custom-cat-1.png"} {
		os.WriteFile(b.path("gallery", name), []byte("img"), 0o644)
	}
	// As the panel leaves it: a custom image, one whose file is gone, a duplicate.
	writeManifest(t, b, `[
		{"id": "custom-cat-1", "title": "cat", "file": "custom-cat-1.png", "custom": true},
		{"id": "custom-gone-2", "title": "gone", "file": "custom-gone-2.png", "custom": true},
		{"id": "custom-cat-1", "title": "cat again", "file": "custom-cat-1.png", "custom": true}
	]`)
	n, err := b.writeManifest()
	if err != nil || n != 2 {
		t.Fatalf("got %d, %v", n, err)
	}
	images := b.ReadManifest()
	if len(images) != 2 || images[0].ID != "horatii" || images[0].File != "horatii.jpg" ||
		images[1].ID != "custom-cat-1" || !images[1].Custom || images[1].Title != "cat" {
		t.Fatalf("unexpected manifest %+v", images)
	}
}

func TestRemoveImage(t *testing.T) {
	b := testBackdrop(t)
	os.MkdirAll(b.path("gallery"), 0o755)
	os.WriteFile(b.path("gallery", "socrates.jpg"), []byte("img"), 0o644)
	os.WriteFile(b.path("gallery", "custom-cat-1.png"), []byte("img"), 0o644)
	writeManifest(t, b, `[{"id": "socrates", "file": "socrates.jpg"},
		{"id": "custom-cat-1", "title": "cat", "file": "custom-cat-1.png", "custom": true}]`)
	b.UpdateConfig(func(c *Config) { c.Image, c.Rotate = "gallery/custom-cat-1.png", "off" })
	r := &silent{}
	if err := b.RemoveImage("socrates", r); err == nil {
		t.Fatal("a built-in painting must not be removable")
	}
	if err := b.RemoveImage("custom-cat-1", r); err != nil {
		t.Fatal(err)
	}
	if len(b.ReadManifest()) != 1 || b.HasPainting("custom-cat-1") {
		t.Fatal("image not removed")
	}
	if !b.ReadConfig().Rotating() {
		t.Fatal("removing the fixed picture should go back to the random pick")
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
		if seen[p.ID] || len(p.Sources) == 0 {
			t.Errorf("bad gallery entry %+v", p)
		}
		seen[p.ID] = true
		for _, s := range p.Sources {
			if !strings.HasPrefix(s, "commons:") && !strings.HasPrefix(s, "search:") && !strings.HasPrefix(s, "met:") && !strings.HasPrefix(s, "https://") {
				t.Errorf("%s: unknown source kind %q", p.ID, s)
			}
			if strings.Contains(s, "?width=") {
				t.Errorf("%s: hardcoded thumbnail width in %q", p.ID, s)
			}
		}
	}
}

func answer(t *testing.T, body string) commonsAnswer {
	t.Helper()
	var a commonsAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func writeJPEG(t *testing.T, file string, w, h int) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
}

func TestOldLowResPaintingsAreUpgraded(t *testing.T) {
	b := testBackdrop(t)
	os.MkdirAll(b.path("gallery"), 0o755)
	writeJPEG(t, b.PaintingFile("socrates"), 2400, 1600)
	writeJPEG(t, b.PaintingFile("horatii"), 1600, 1066)
	if longSide(b.PaintingFile("socrates")) != 2400 || longSide(b.PaintingFile("horatii")) != 1600 {
		t.Fatal("longSide misread the pictures")
	}
	socrates, _ := PaintingByID("socrates")
	r := &silent{}
	if err := b.ensurePainting(socrates, r); err != nil || len(r.lines) != 0 {
		t.Fatalf("a large enough painting must stay as is: %v %v", err, r.lines)
	}
	// The 1600 px one would be downloaded again (no network here: only check
	// that it tries, and that the old file survives the failure).
	horatii, _ := PaintingByID("horatii")
	horatii.Sources = []string{"https://127.0.0.1:1/nothing.jpg"}
	if err := b.ensurePainting(horatii, r); err == nil || !strings.Contains(strings.Join(r.lines, "\n"), "haute définition") {
		t.Fatalf("a 1600 px painting should be fetched again, got %v %v", err, r.lines)
	}
	if longSide(b.PaintingFile("horatii")) != 1600 {
		t.Fatal("a failed download must keep the old picture")
	}
}

func TestParseCommons(t *testing.T) {
	thumb := answer(t, `{"batchcomplete":true,"query":{"pages":[{"ns":6,"title":"File:A.jpg","imageinfo":[
		{"thumburl":"https://upload.wikimedia.org/thumb/a/a1/A.jpg/1920px-A.jpg","thumbwidth":1920,"url":"https://upload.wikimedia.org/a/a1/A.jpg"}]}]}}`)
	if got, err := parseCommonsFile(thumb); err != nil || !strings.Contains(got, "1920px") {
		t.Fatalf("got %q, %v", got, err)
	}
	original := answer(t, `{"query":{"pages":[{"title":"File:B.jpg","imageinfo":[{"url":"https://upload.wikimedia.org/b/B.jpg"}]}]}}`)
	if got, err := parseCommonsFile(original); err != nil || got != "https://upload.wikimedia.org/b/B.jpg" {
		t.Fatalf("got %q, %v", got, err)
	}
	missing := answer(t, `{"query":{"pages":[{"ns":6,"title":"File:Nope.jpg","missing":true,"known":false}]}}`)
	if _, err := parseCommonsFile(missing); err == nil || !strings.Contains(err.Error(), "n'existe pas") {
		t.Fatalf("a missing file should say so, got %v", err)
	}
	// Search: best ranked photo wins, a PDF or an SVG is skipped.
	search := answer(t, `{"query":{"pages":[
		{"title":"File:C.jpg","index":3,"imageinfo":[{"url":"https://u/C.jpg","thumburl":"https://u/1920px-C.jpg"}]},
		{"title":"File:Scan.pdf","index":1,"imageinfo":[{"url":"https://u/Scan.pdf","thumburl":"https://u/page1-1920px-Scan.pdf.jpg"}]},
		{"title":"File:D.JPG","index":2,"imageinfo":[{"url":"https://u/D.JPG","thumburl":"https://u/1920px-D.JPG"}]}]}}`)
	if got, err := parseCommonsSearch(search, "x"); err != nil || got != "https://u/1920px-D.JPG" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseCommonsSearch(answer(t, `{"batchcomplete":true}`), "rien"); err == nil {
		t.Fatal("an empty search should fail")
	}
}

func TestSlugMatchesTheLoader(t *testing.T) {
	for in, want := range map[string]string{"My Picture": "my-picture", "  Été 2024 !! ": "t-2024", "": "image", "---": "image"} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slug(strings.Repeat("a", 50)); len(got) != 32 {
		t.Errorf("slug should cap at 32 characters, got %d", len(got))
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
	if st.AppFound || st.Loader != LoaderMissing || st.Config != Defaults() || !strings.HasPrefix(st.Picture, "au hasard") {
		t.Fatalf("unexpected status %+v", st)
	}
}
