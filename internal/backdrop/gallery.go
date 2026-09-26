package backdrop

// The background pictures, all kept in <Dir>/gallery and listed in
// gallery/manifest.json: the built-in public-domain paintings, plus your own
// images, added here or from the gallery panel inside Claude (entries marked
// `custom`). The loader ships every listed picture to the page, which draws
// one per conversation, or shows the fixed one (config `image`). Downloads are
// converted to JPEG with macOS `sips` (plain copy elsewhere).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/M-U-C-K-A/claude-code/internal/macos"
)

// Painting is a built-in gallery entry.
type Painting struct {
	ID     string
	Title  string
	Artist string
	Year   int
	// Tried in order, the first that downloads wins: "commons:<file>" (a
	// Wikimedia Commons file, resolved through its API), "search:<words>" (the
	// best matching Commons file), "met:<object id>" (The Met's open-access
	// API), or a plain URL.
	Sources []string
}

func (p Painting) Label() string {
	return fmt.Sprintf("%s — %s, %d", p.Title, p.Artist, p.Year)
}

// Gallery lists the built-in paintings. File names checked against Commons;
// the search is a last resort should a file be renamed.
var Gallery = []Painting{
	{
		ID: "socrates", Title: "La Mort de Socrate", Artist: "Jacques-Louis David", Year: 1787,
		Sources: []string{"commons:David - The Death of Socrates.jpg", "met:436105"},
	},
	{
		ID: "horatii", Title: "Le Serment des Horaces", Artist: "Jacques-Louis David", Year: 1784,
		Sources: []string{
			"commons:David-Oath of the Horatii-1784.jpg",
			"commons:Jacques-Louis David - Oath of the Horatii - Google Art Project.jpg",
			"search:Jacques-Louis David Oath of the Horatii",
		},
	},
	{
		ID: "pandemonium", Title: "Pandémonium", Artist: "John Martin", Year: 1841,
		Sources: []string{
			"commons:John Martin - Pandemonium - WGA14149.jpg",
			"commons:John Martin Le Pandemonium Louvre.JPG",
			"commons:John-Martin-Pandemonium-color-sharpend.jpg",
			"search:John Martin Pandemonium 1841",
		},
	},
	{
		ID: "school-of-athens", Title: "L'École d'Athènes", Artist: "Raphaël", Year: 1511,
		Sources: []string{
			`commons:"The School of Athens" by Raffaello Sanzio da Urbino.jpg`,
			"commons:Raphael School of Athens.jpg",
			"search:Raphael School of Athens Stanza",
		},
	},
}

// PaintingByID finds a built-in painting (a few French aliases accepted).
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

// IsRandomChoice tells whether the user asked for a picture per conversation.
func IsRandomChoice(spec string) bool {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "random", "hasard", "aleatoire", "aléatoire", "rotation", "galerie", "gallery":
		return true
	}
	return false
}

// PaintingFile is where a built-in painting is stored once downloaded.
func (b *Backdrop) PaintingFile(id string) string { return b.path("gallery", id+".jpg") }

func (b *Backdrop) HasPainting(id string) bool {
	_, err := os.Stat(b.PaintingFile(id))
	return err == nil
}

// ---------------------------------------------------------------- manifest

// Image is an entry of gallery/manifest.json.
type Image struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	File   string `json:"file"`
	Custom bool   `json:"custom,omitempty"`
}

// ImageFile is where a gallery picture sits on disk.
func (b *Backdrop) ImageFile(img Image) string { return b.path("gallery", img.File) }

// ReadManifest lists the pictures in the gallery (only those on disk).
func (b *Backdrop) ReadManifest() []Image {
	data, err := os.ReadFile(b.path("gallery", "manifest.json"))
	if err != nil {
		return nil
	}
	var entries []Image
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	var out []Image
	seen := map[string]bool{}
	for _, e := range entries {
		if e.ID == "" || seen[e.ID] {
			continue
		}
		if e.File == "" {
			e.File = e.ID + ".jpg"
		}
		e.File = path.Base(e.File)
		if _, err := os.Stat(b.ImageFile(e)); err != nil {
			continue
		}
		if e.Title == "" {
			e.Title = e.ID
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out
}

// writeManifest lists the built-in paintings on disk, then the custom images
// already listed (the panel in Claude adds some), deduplicated. Returns how
// many pictures the gallery holds.
func (b *Backdrop) writeManifest() (int, error) {
	if err := os.MkdirAll(b.path("gallery"), 0o755); err != nil {
		return 0, err
	}
	manifest := []Image{}
	seen := map[string]bool{}
	for _, p := range Gallery {
		if b.HasPainting(p.ID) {
			manifest = append(manifest, Image{ID: p.ID, Title: p.Label(), File: p.ID + ".jpg"})
			seen[p.ID] = true
		}
	}
	for _, img := range b.ReadManifest() {
		if img.Custom && !seen[img.ID] {
			manifest = append(manifest, img)
			seen[img.ID] = true
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return 0, err
	}
	return len(manifest), writeAtomic(b.path("gallery", "manifest.json"), append(data, '\n'))
}

// FixedID is the gallery id of the fixed picture, or "" when it is not one
// (the default background.<ext>, set from the panel's "Définir par défaut").
func (b *Backdrop) FixedID(cfg Config) string {
	file, ok := strings.CutPrefix(cfg.Image, "gallery/")
	if !ok {
		return ""
	}
	for _, img := range b.ReadManifest() {
		if img.File == file {
			return img.ID
		}
	}
	return ""
}

// PictureSummary says in a few words what is behind the conversations.
func (b *Backdrop) PictureSummary(cfg Config) string {
	if cfg.Rotating() {
		n := len(b.ReadManifest())
		if n == 0 {
			return "au hasard — galerie vide pour l'instant"
		}
		return fmt.Sprintf("au hasard, une par conversation (%d images)", n)
	}
	if id := b.FixedID(cfg); id != "" {
		if p, ok := PaintingByID(id); ok {
			return "fixe — " + p.Title
		}
		for _, img := range b.ReadManifest() {
			if img.ID == id {
				return "fixe — " + img.Title
			}
		}
	}
	return "fixe — image par défaut"
}

// ---------------------------------------------------------------- downloads

// commonsWidth must be one of Wikimedia's standard thumbnail steps (…, 1280,
// 1920, 3840): since 2025 any other width is refused with HTTP 429.
const commonsWidth = 1920

const (
	userAgent      = "claude-backdrop/" + Version + " (https://github.com/M-U-C-K-A/claude-code)"
	maxDownload    = 40 << 20
	maxRawBytes    = 16 << 20 // without sips, the picture is used as is (the loader's limit)
	galleryMaxSide = 1600     // every gallery picture ships to each page
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

type commonsPage struct {
	Title     string `json:"title"`
	Index     int    `json:"index"`
	Missing   bool   `json:"missing"`
	Invalid   bool   `json:"invalid"`
	ImageInfo []struct {
		URL      string `json:"url"`
		ThumbURL string `json:"thumburl"`
	} `json:"imageinfo"`
}

type commonsAnswer struct {
	Query struct {
		Pages []commonsPage `json:"pages"`
	} `json:"query"`
}

func commonsQuery(params url.Values) (commonsAnswer, error) {
	params.Set("action", "query")
	params.Set("format", "json")
	params.Set("formatversion", "2")
	params.Set("prop", "imageinfo")
	params.Set("iiprop", "url")
	params.Set("iiurlwidth", strconv.Itoa(commonsWidth))
	var body json.RawMessage
	if err := getJSON("https://commons.wikimedia.org/w/api.php?"+params.Encode(), &body); err != nil {
		return commonsAnswer{}, fmt.Errorf("API Commons : %w", err)
	}
	var answer commonsAnswer
	if err := json.Unmarshal(body, &answer); err != nil {
		return answer, fmt.Errorf("réponse Commons illisible : %w", err)
	}
	return answer, nil
}

// bestURL is the 1920 px rendition of a page (or the original when smaller).
func (p commonsPage) bestURL() string {
	for _, info := range p.ImageInfo {
		if info.ThumbURL != "" {
			return info.ThumbURL
		}
		if info.URL != "" {
			return info.URL
		}
	}
	return ""
}

// parseCommonsFile picks the image of a single-file answer.
func parseCommonsFile(answer commonsAnswer) (string, error) {
	if len(answer.Query.Pages) == 0 {
		return "", errors.New("réponse Commons vide")
	}
	page := answer.Query.Pages[0]
	if page.Missing || page.Invalid {
		return "", fmt.Errorf("%s n'existe pas sur Commons", page.Title)
	}
	if u := page.bestURL(); u != "" {
		return u, nil
	}
	return "", errors.New("pas d'image dans la réponse Commons")
}

var photoRE = regexp.MustCompile(`(?i)\.(jpe?g|png)$`)

// parseCommonsSearch picks the best ranked JPEG or PNG of a search answer.
func parseCommonsSearch(answer commonsAnswer, words string) (string, error) {
	pages := answer.Query.Pages
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Index < pages[j].Index })
	for _, page := range pages {
		for _, info := range page.ImageInfo {
			if photoRE.MatchString(strings.SplitN(info.URL, "?", 2)[0]) {
				return page.bestURL(), nil
			}
		}
	}
	return "", fmt.Errorf("aucune image pour « %s »", words)
}

// fetchImage downloads one source into a temporary file.
func fetchImage(source string) (string, error) {
	target := source
	var err error
	switch {
	case strings.HasPrefix(source, "commons:"):
		var answer commonsAnswer
		if answer, err = commonsQuery(url.Values{"redirects": {"1"}, "titles": {"File:" + strings.TrimPrefix(source, "commons:")}}); err == nil {
			target, err = parseCommonsFile(answer)
		}
	case strings.HasPrefix(source, "search:"):
		words := strings.TrimPrefix(source, "search:")
		var answer commonsAnswer
		if answer, err = commonsQuery(url.Values{"generator": {"search"}, "gsrnamespace": {"6"}, "gsrlimit": {"8"}, "gsrsearch": {words}}); err == nil {
			target, err = parseCommonsSearch(answer, words)
		}
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
	case strings.HasPrefix(source, "search:"):
		return "recherche Commons « " + strings.TrimPrefix(source, "search:") + " »"
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
	var facts ImageFacts
	if macos.HasSips() {
		if facts.Width, facts.Height, err = macos.ToJpeg(input, staging, maxSide); err != nil {
			return ImageFacts{}, err
		}
	} else {
		if info.Size() > maxRawBytes {
			return ImageFacts{}, userErrorf("image trop lourde (16 Mo max sans sips)")
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

// ---------------------------------------------------------------- choosing

var (
	urlRE     = regexp.MustCompile(`(?i)^https?://`)
	nonSlugRE = regexp.MustCompile(`[^a-z0-9]+`)
)

// slug is the loader's: lowercase words joined by dashes, 32 characters max.
func slug(s string) string {
	s = strings.Trim(nonSlugRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	if s == "" {
		return "image"
	}
	return s
}

// ensurePainting downloads a built-in painting into the gallery if needed.
func (b *Backdrop) ensurePainting(p Painting, r Reporter) error {
	if b.HasPainting(p.ID) {
		return nil
	}
	r.Step("Téléchargement : " + p.Label())
	tmp, err := Download(p.Sources)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(tmp))
	if _, err := convert(tmp, b.PaintingFile(p.ID), galleryMaxSide); err != nil {
		return err
	}
	_, err = b.writeManifest()
	return err
}

// AddImage puts your own picture (a file or a URL) in the gallery, like the
// ＋ tile of the panel in Claude does.
func (b *Backdrop) AddImage(spec string, r Reporter) (Image, error) {
	if err := b.EnsureSupport(); err != nil {
		return Image{}, err
	}
	spec = strings.TrimSpace(spec)
	var input, name string
	if urlRE.MatchString(spec) {
		r.Step("Téléchargement de " + spec)
		tmp, err := Download([]string{spec})
		if err != nil {
			return Image{}, err
		}
		defer os.RemoveAll(filepath.Dir(tmp))
		input = tmp
		if u, err := url.Parse(spec); err == nil {
			name = path.Base(u.Path)
		}
	} else {
		abs, err := filepath.Abs(ExpandHome(spec))
		if err != nil {
			return Image{}, err
		}
		if _, err := os.Stat(abs); err != nil {
			return Image{}, userErrorf("Fichier introuvable : %s", abs)
		}
		input, name = abs, filepath.Base(abs)
	}
	title := strings.TrimSuffix(name, path.Ext(name))
	if title == "" || title == "." || title == "/" {
		title = "Mon image"
	}
	ext := ".jpg"
	if !macos.HasSips() {
		ext = strings.ToLower(filepath.Ext(input))
	}
	img := Image{ID: "custom-" + slug(title) + "-" + strconv.FormatInt(time.Now().UnixMilli(), 36), Title: title, Custom: true}
	img.File = img.ID + ext
	facts, err := convert(input, b.ImageFile(img), galleryMaxSide)
	if err != nil {
		return Image{}, err
	}
	manifest := append(b.ReadManifest(), img)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err == nil {
		err = writeAtomic(b.path("gallery", "manifest.json"), append(data, '\n'))
	}
	if err != nil {
		return Image{}, err
	}
	r.OK(fmt.Sprintf("Ajoutée à la galerie : %s (%s)", title, facts))
	return img, nil
}

// SetFixed puts one gallery picture behind every conversation (turns the
// random pick off). A built-in painting is downloaded first if needed.
func (b *Backdrop) SetFixed(id string, r Reporter) error {
	if err := b.EnsureSupport(); err != nil {
		return err
	}
	title := ""
	if p, ok := PaintingByID(id); ok {
		if err := b.ensurePainting(p, r); err != nil {
			return err
		}
		id, title = p.ID, p.Title
	}
	for _, img := range b.ReadManifest() {
		if img.ID != id {
			continue
		}
		if title == "" {
			title = img.Title
		}
		if _, err := b.UpdateConfig(func(c *Config) { c.Image, c.Rotate = "gallery/"+img.File, "off" }); err != nil {
			return err
		}
		r.OK("Image fixe : " + title)
		return nil
	}
	return userErrorf("« %s » n'est pas dans la galerie", id)
}

// SetRotation gives each conversation its own picture, downloading the missing
// paintings first.
func (b *Backdrop) SetRotation(r Reporter) error {
	if err := b.EnsureSupport(); err != nil {
		return err
	}
	available, err := b.EnsureGallery(r)
	if err != nil {
		return err
	}
	if _, err := b.UpdateConfig(func(c *Config) { c.Rotate = "conversation" }); err != nil {
		return err
	}
	if available == 0 {
		r.Warn("Galerie vide : Claude garde l'image par défaut en attendant.")
		return nil
	}
	r.OK(fmt.Sprintf("Une image au hasard par conversation (%d dans la galerie)", available))
	return nil
}

// RemoveImage takes one of your images out of the gallery (the built-in
// paintings stay).
func (b *Backdrop) RemoveImage(id string, r Reporter) error {
	var kept []Image
	var removed *Image
	for _, img := range b.ReadManifest() {
		if img.ID == id && img.Custom {
			removed = &img
			continue
		}
		kept = append(kept, img)
	}
	if removed == nil {
		return userErrorf("Seules tes propres images se retirent de la galerie.")
	}
	data, err := json.MarshalIndent(append([]Image{}, kept...), "", "  ")
	if err == nil {
		err = writeAtomic(b.path("gallery", "manifest.json"), append(data, '\n'))
	}
	if err != nil {
		return err
	}
	os.Remove(b.ImageFile(*removed))
	if cfg := b.ReadConfig(); cfg.Image == "gallery/"+removed.File {
		if _, err := b.UpdateConfig(func(c *Config) { c.Rotate = "conversation" }); err != nil {
			return err
		}
		r.Note("C'était l'image fixe : retour au hasard.")
	}
	r.OK("Retirée de la galerie : " + removed.Title)
	return nil
}

// ChooseImage handles `claude-backdrop image <choice>`: "hasard", a gallery
// id, or a file / URL to add and show.
func (b *Backdrop) ChooseImage(spec string, r Reporter) error {
	if IsRandomChoice(spec) {
		return b.SetRotation(r)
	}
	if _, ok := PaintingByID(spec); ok {
		return b.SetFixed(spec, r)
	}
	for _, img := range b.ReadManifest() {
		if img.ID == spec {
			return b.SetFixed(spec, r)
		}
	}
	img, err := b.AddImage(spec, r)
	if err != nil {
		return err
	}
	return b.SetFixed(img.ID, r)
}

// EnsureGallery downloads the missing paintings and rewrites the manifest the
// loader reads. Returns how many pictures the gallery holds.
func (b *Backdrop) EnsureGallery(r Reporter) (int, error) {
	if err := os.MkdirAll(b.path("gallery"), 0o755); err != nil {
		return 0, err
	}
	for _, p := range Gallery {
		if err := b.ensurePainting(p, r); err != nil {
			r.Warn(fmt.Sprintf("%s indisponible : %s", p.Title, err))
		}
	}
	return b.writeManifest()
}
