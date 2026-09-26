package backdrop

// The background pictures: a small built-in gallery of public-domain paintings,
// plus any file or URL the user points at. Downloads are converted to JPEG with
// macOS `sips` (plain copy elsewhere).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/M-U-C-K-A/claude-code/internal/macos"
)

// Painting is a gallery entry. Mode says which Claude appearance it suits: the
// dark, dramatic ones for dark mode, the bright fresco for light mode.
type Painting struct {
	ID     string
	Mode   string // "dark" or "light"
	Title  string
	Artist string
	Year   int
	// Tried in order, the first that downloads wins: "commons:<file>" (a
	// Wikimedia Commons file, resolved through its API), "met:<object id>"
	// (The Met's open-access API), or a plain URL.
	Sources []string
}

func (p Painting) Label() string {
	return fmt.Sprintf("%s — %s, %d", p.Title, p.Artist, p.Year)
}

// Gallery lists the built-in paintings. File names checked against Commons.
var Gallery = []Painting{
	{
		ID: "socrates", Mode: "dark", Title: "La Mort de Socrate", Artist: "Jacques-Louis David", Year: 1787,
		Sources: []string{"commons:David - The Death of Socrates.jpg", "met:436105"},
	},
	{
		ID: "horatii", Mode: "dark", Title: "Le Serment des Horaces", Artist: "Jacques-Louis David", Year: 1784,
		Sources: []string{
			"commons:David-Oath of the Horatii-1784.jpg",
			"commons:Jacques-Louis David - Oath of the Horatii - Google Art Project.jpg",
		},
	},
	{
		ID: "pandemonium", Mode: "dark", Title: "Pandémonium", Artist: "John Martin", Year: 1841,
		Sources: []string{
			"commons:John Martin - Pandemonium - WGA14149.jpg",
			"commons:John Martin Le Pandemonium Louvre.JPG",
			"commons:John-Martin-Pandemonium-color-sharpend.jpg",
		},
	},
	{
		ID: "school-of-athens", Mode: "light", Title: "L'École d'Athènes", Artist: "Raphaël", Year: 1511,
		Sources: []string{
			`commons:"The School of Athens" by Raffaello Sanzio da Urbino.jpg`,
			"commons:Raphael School of Athens.jpg",
		},
	},
}

// PaintingByID finds a gallery entry (a few French aliases accepted).
func PaintingByID(id string) (Painting, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	switch id {
	case "socrate", "defaut", "défaut", "default":
		id = "socrates"
	case "horaces":
		id = "horatii"
	case "athenes", "athènes", "ecole", "école", "school":
		id = "school-of-athens"
	}
	for _, p := range Gallery {
		if p.ID == id {
			return p, true
		}
	}
	return Painting{}, false
}

// IsRandomChoice tells whether the user asked for a painting per conversation.
func IsRandomChoice(spec string) bool {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "random", "hasard", "aleatoire", "aléatoire", "rotation", "galerie", "gallery":
		return true
	}
	return false
}

// PaintingFile is where a gallery painting is stored once downloaded.
func (b *Backdrop) PaintingFile(id string) string { return b.path("gallery", id+".jpg") }

func (b *Backdrop) HasPainting(id string) bool {
	_, err := os.Stat(b.PaintingFile(id))
	return err == nil
}

// ---------------------------------------------------------------- downloads

// commonsWidth must be one of Wikimedia's standard thumbnail steps (…, 1280,
// 1920, 3840): since 2025 any other width is refused with HTTP 429.
const commonsWidth = 1920

const (
	userAgent      = "claude-backdrop/" + Version + " (https://github.com/M-U-C-K-A/claude-code)"
	maxDownload    = 40 << 20
	maxRawBytes    = 24 << 20 // without sips, the picture is used as is
	fixedMaxSide   = 2560
	galleryMaxSide = 1600 // smaller: every gallery picture ships to each page
)

var client = &http.Client{Timeout: 90 * time.Second}

var extensions = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp",
	"image/gif": ".gif", "image/avif": ".avif", "image/heic": ".heic",
}

func get(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // the cause, without the (long) URL
		}
		return nil, fmt.Errorf("réseau : %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, errors.New("HTTP 429, le serveur limite les téléchargements : réessaie dans une minute")
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func getJSON(rawURL string, into any) error {
	resp, err := get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into)
}

// commonsImageURL asks the Commons API for a 1920 px rendition of a file (or
// the original when it is smaller). The API follows renames and says clearly
// when a file does not exist.
func commonsImageURL(file string) (string, error) {
	query := url.Values{
		"action": {"query"}, "format": {"json"}, "formatversion": {"2"}, "redirects": {"1"},
		"prop": {"imageinfo"}, "iiprop": {"url|mime"}, "iiurlwidth": {strconv.Itoa(commonsWidth)},
		"titles": {"File:" + file},
	}
	var body json.RawMessage
	if err := getJSON("https://commons.wikimedia.org/w/api.php?"+query.Encode(), &body); err != nil {
		return "", fmt.Errorf("API Commons : %w", err)
	}
	return parseCommons(body)
}

func parseCommons(body []byte) (string, error) {
	var answer struct {
		Query struct {
			Pages []struct {
				Title     string `json:"title"`
				Missing   bool   `json:"missing"`
				Invalid   bool   `json:"invalid"`
				ImageInfo []struct {
					URL      string `json:"url"`
					ThumbURL string `json:"thumburl"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return "", fmt.Errorf("réponse Commons illisible : %w", err)
	}
	if len(answer.Query.Pages) == 0 {
		return "", errors.New("réponse Commons vide")
	}
	page := answer.Query.Pages[0]
	if page.Missing || page.Invalid {
		return "", fmt.Errorf("%s n'existe pas sur Commons", page.Title)
	}
	for _, info := range page.ImageInfo {
		if info.ThumbURL != "" {
			return info.ThumbURL, nil
		}
		if info.URL != "" {
			return info.URL, nil
		}
	}
	return "", errors.New("pas d'image dans la réponse Commons")
}

func metImageURL(id string) (string, error) {
	var object struct {
		PrimaryImage      string `json:"primaryImage"`
		PrimaryImageSmall string `json:"primaryImageSmall"`
	}
	if err := getJSON("https://collectionapi.metmuseum.org/public/collection/v1/objects/"+id, &object); err != nil {
		return "", fmt.Errorf("API du Met : %w", err)
	}
	if object.PrimaryImage != "" {
		return object.PrimaryImage, nil
	}
	if object.PrimaryImageSmall != "" {
		return object.PrimaryImageSmall, nil
	}
	return "", errors.New("API du Met : pas d'image")
}

// fetchImage downloads one source into a temporary file.
func fetchImage(source string) (string, error) {
	target := source
	var err error
	switch {
	case strings.HasPrefix(source, "commons:"):
		target, err = commonsImageURL(strings.TrimPrefix(source, "commons:"))
	case strings.HasPrefix(source, "met:"):
		target, err = metImageURL(strings.TrimPrefix(source, "met:"))
	}
	if err != nil {
		return "", err
	}
	resp, err := get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	mime := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if !strings.HasPrefix(mime, "image/") {
		if mime == "" {
			mime = "type inconnu"
		}
		return "", fmt.Errorf("réponse non-image (%s)", mime)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxDownload {
		return "", errors.New("image trop lourde (40 Mo max)")
	}
	dir, err := os.MkdirTemp("", "claude-backdrop-")
	if err != nil {
		return "", err
	}
	ext := extensions[mime]
	if ext == "" {
		ext = ".img"
	}
	file := filepath.Join(dir, "image"+ext)
	return file, os.WriteFile(file, data, 0o600)
}

// Download tries each source in order; the error lists why each one failed.
func Download(sources []string) (string, error) {
	var failures []string
	for _, source := range sources {
		file, err := fetchImage(source)
		if err == nil {
			return file, nil
		}
		failures = append(failures, fmt.Sprintf("  - %s : %v", sourceName(source), err))
	}
	return "", fmt.Errorf("téléchargement impossible :\n%s", strings.Join(failures, "\n"))
}

// sourceName is a source as shown in error messages.
func sourceName(source string) string {
	switch {
	case strings.HasPrefix(source, "commons:"):
		return "Wikimedia Commons « " + strings.TrimPrefix(source, "commons:") + " »"
	case strings.HasPrefix(source, "met:"):
		return "The Met (objet " + strings.TrimPrefix(source, "met:") + ")"
	}
	if r := []rune(source); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return source
}

// ImageFacts describes a stored picture.
type ImageFacts struct {
	Name          string
	Width, Height int
	Bytes         int64
}

func (f ImageFacts) String() string {
	size := ""
	if f.Width > 0 {
		size = fmt.Sprintf("%d×%d, ", f.Width, f.Height)
	}
	return fmt.Sprintf("%s%d Ko", size, f.Bytes/1024)
}

// convert writes input as a JPEG at dest, longest side at most maxSide. Falls
// back to a plain copy when sips is missing (no resize, no conversion).
func convert(input, dest string, maxSide int) (ImageFacts, error) {
	info, err := os.Stat(input)
	if err != nil || !info.Mode().IsRegular() {
		return ImageFacts{}, userErrorf("%s n'est pas un fichier", input)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return ImageFacts{}, err
	}
	staging := dest + ".new"
	facts := ImageFacts{Name: filepath.Base(dest)}
	if macos.HasSips() {
		if facts.Width, facts.Height, err = macos.ToJpeg(input, staging, maxSide); err != nil {
			return ImageFacts{}, err
		}
	} else {
		if info.Size() > maxRawBytes {
			return ImageFacts{}, userErrorf("image trop lourde (24 Mo max sans sips)")
		}
		data, err := os.ReadFile(input)
		if err != nil {
			return ImageFacts{}, err
		}
		if err := os.WriteFile(staging, data, 0o644); err != nil {
			return ImageFacts{}, err
		}
	}
	if err := os.Rename(staging, dest); err != nil {
		return ImageFacts{}, err
	}
	if info, err := os.Stat(dest); err == nil {
		facts.Bytes = info.Size()
	}
	return facts, nil
}

var (
	backgroundRE = regexp.MustCompile(`(?i)^background\.[a-z0-9]+$`)
	urlRE        = regexp.MustCompile(`(?i)^https?://`)
)

// storeFixed saves the chosen fixed picture as background.jpg in the support
// folder, and removes older background.* files.
func (b *Backdrop) storeFixed(input string) (ImageFacts, error) {
	name := "background.jpg"
	if !macos.HasSips() {
		ext := strings.ToLower(filepath.Ext(input))
		if ext == "" {
			ext = ".jpg"
		}
		name = "background" + ext
	}
	facts, err := convert(input, b.path(name), fixedMaxSide)
	if err != nil {
		return facts, err
	}
	entries, _ := os.ReadDir(b.Dir)
	for _, e := range entries {
		if e.Name() != name && backgroundRE.MatchString(e.Name()) {
			os.Remove(b.path(e.Name()))
		}
	}
	return facts, nil
}

// ---------------------------------------------------------------- choosing

// SetFixedImage puts one picture behind every conversation (turns rotation
// off): a gallery id, a file, or a URL.
func (b *Backdrop) SetFixedImage(spec string, r Reporter) error {
	if err := b.EnsureSupport(); err != nil {
		return err
	}
	var input, label, source string
	var err error
	downloaded := false
	spec = strings.TrimSpace(spec)
	if painting, ok := PaintingByID(spec); ok {
		label, source = painting.Label(), painting.ID
		r.Step("Téléchargement : " + label)
		input, err = Download(painting.Sources)
		downloaded = err == nil
		if err != nil && b.HasPainting(painting.ID) {
			r.Warn("Téléchargement impossible, j'utilise la copie de la galerie (plus petite).")
			input, err = b.PaintingFile(painting.ID), nil
		}
	} else if urlRE.MatchString(spec) {
		label, source = spec, spec
		r.Step("Téléchargement de " + spec)
		input, err = Download([]string{spec})
		downloaded = err == nil
	} else {
		input, err = filepath.Abs(ExpandHome(spec))
		label, source = filepath.Base(input), filepath.Base(input)
		if err == nil {
			if _, statErr := os.Stat(input); statErr != nil {
				err = userErrorf("Fichier introuvable : %s", input)
			}
		}
	}
	if err != nil {
		return err
	}
	if downloaded {
		defer os.RemoveAll(filepath.Dir(input))
	}
	facts, err := b.storeFixed(input)
	if err != nil {
		return err
	}
	// A gallery painting fetched for the first time joins the gallery too
	// (preview, rotation), so it is not downloaded twice.
	if painting, ok := PaintingByID(source); ok && downloaded && !b.HasPainting(painting.ID) {
		if _, err := convert(input, b.PaintingFile(painting.ID), galleryMaxSide); err == nil {
			b.writeManifest()
		}
	}
	if _, err := b.UpdateConfig(func(c *Config) { c.Image, c.ImageSource, c.Rotate = facts.Name, source, "off" }); err != nil {
		return err
	}
	r.OK(fmt.Sprintf("Image fixe : %s (%s)", label, facts))
	return nil
}

// SetRotation gives each conversation its own painting, downloading the
// missing ones first.
func (b *Backdrop) SetRotation(r Reporter) error {
	if err := b.EnsureSupport(); err != nil {
		return err
	}
	available, err := b.EnsureGallery(r, false)
	if err != nil {
		return err
	}
	if _, err := b.UpdateConfig(func(c *Config) { c.Rotate = "conversation" }); err != nil {
		return err
	}
	if available == 0 {
		r.Warn("Aucun tableau téléchargé : Claude garde l'image fixe en attendant.")
		return nil
	}
	r.OK(fmt.Sprintf("Un tableau au hasard par conversation (%d disponibles)", available))
	return nil
}

// ChooseImage handles `claude-backdrop image <choice>`.
func (b *Backdrop) ChooseImage(spec string, r Reporter) error {
	if IsRandomChoice(spec) {
		return b.SetRotation(r)
	}
	return b.SetFixedImage(spec, r)
}

// EnsureGallery downloads the missing paintings (all of them with force) and
// rewrites the manifest the loader reads. Returns how many are available.
func (b *Backdrop) EnsureGallery(r Reporter, force bool) (int, error) {
	if err := os.MkdirAll(b.path("gallery"), 0o755); err != nil {
		return 0, err
	}
	for _, painting := range Gallery {
		dest := b.PaintingFile(painting.ID)
		if !force && b.HasPainting(painting.ID) {
			continue
		}
		r.Step("Téléchargement : " + painting.Label())
		tmp, err := Download(painting.Sources)
		if err == nil {
			_, err = convert(tmp, dest, galleryMaxSide)
			os.RemoveAll(filepath.Dir(tmp))
		}
		if err != nil {
			r.Warn(fmt.Sprintf("%s indisponible : %s", painting.Title, err))
		}
	}
	return b.writeManifest()
}

// writeManifest lists the paintings actually on disk, each with its light/dark
// tag, so the loader offers only those.
func (b *Backdrop) writeManifest() (int, error) {
	type entry struct {
		ID    string `json:"id"`
		Mode  string `json:"mode"`
		Title string `json:"title"`
	}
	manifest := []entry{}
	for _, painting := range Gallery {
		if b.HasPainting(painting.ID) {
			manifest = append(manifest, entry{painting.ID, painting.Mode, painting.Label()})
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return 0, err
	}
	return len(manifest), writeAtomic(b.path("gallery", "manifest.json"), append(data, '\n'))
}
