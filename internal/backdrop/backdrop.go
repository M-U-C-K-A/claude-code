// Package backdrop is claude-backdrop itself: the support folder the loader
// reads (config, theme, pictures), the painting gallery, and installing /
// restoring the loader in Claude.app. The command line and the TUI are thin
// layers over it. User-facing messages are in French.
package backdrop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	payload "github.com/M-U-C-K-A/claude-code/src"
)

const Version = "2.0.0"

const DefaultApp = "/Applications/Claude.app"

// Reporter receives progress lines from long operations (install, downloads).
type Reporter interface {
	Step(text string) // something starts
	OK(text string)   // something succeeded
	Warn(text string) // worth knowing, not fatal
	Note(text string) // secondary detail
}

// UserError is a failure to show as is, without a stack trace.
type UserError struct{ msg string }

func (e *UserError) Error() string { return e.msg }

func userErrorf(format string, args ...any) error {
	return &UserError{fmt.Sprintf(format, args...)}
}

// IsUserError tells whether err is meant to be shown as is.
func IsUserError(err error) bool {
	var ue *UserError
	return errors.As(err, &ue)
}

// Backdrop ties the support folder to one Claude.app.
type Backdrop struct {
	Dir string // ~/Library/Application Support/ClaudeBackdrop
	App string // /Applications/Claude.app
}

// New resolves the folders: app (or $CLAUDE_APP, or /Applications/Claude.app),
// and $CLAUDE_BACKDROP_DIR or the default support folder.
func New(app string) *Backdrop {
	if app == "" {
		app = os.Getenv("CLAUDE_APP")
	}
	if app == "" {
		app = DefaultApp
	}
	if abs, err := filepath.Abs(ExpandHome(app)); err == nil {
		app = abs
	}
	dir := os.Getenv("CLAUDE_BACKDROP_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "Library", "Application Support", "ClaudeBackdrop")
	}
	return &Backdrop{Dir: dir, App: app}
}

func (b *Backdrop) path(parts ...string) string {
	return filepath.Join(append([]string{b.Dir}, parts...)...)
}

// ExpandHome turns a leading ~/ into the home folder.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

func requireMac() error {
	if runtime.GOOS != "darwin" {
		return userErrorf("Ça ne tourne que sur macOS, là où est installé Claude Desktop.")
	}
	return nil
}

// ---------------------------------------------------------------- config

// Config is config.json, read live by the loader inside Claude. The loader
// writes it too (the gallery panel in Claude), so keys this version does not
// know are kept as they are.
type Config struct {
	Enabled         bool    `json:"enabled"`
	Image           string  `json:"image"`  // the fixed picture, relative to Dir ("gallery/<file>" or "background.<ext>")
	Rotate          string  `json:"rotate"` // "conversation" = a random picture per conversation, "off" = fixed
	Dim             float64 `json:"dim"`
	Brightness      float64 `json:"brightness"`
	ImageOpacity    float64 `json:"imageOpacity"`
	ImageBlur       float64 `json:"imageBlur"`
	Glass           float64 `json:"glass"`
	Blur            float64 `json:"blur"`
	TerminalOpacity float64 `json:"terminalOpacity"`
	Position        string  `json:"position"`
	Size            string  `json:"size"`
	Mode            string  `json:"mode"` // "dark", "light" or "auto": exposed to the page as data-cb-mode
	AutoClear       bool    `json:"autoClear"`
	Refresh         int64   `json:"refresh,omitempty"` // bumped to ask the loader for a fresh report
}

func Defaults() Config {
	return Config{
		Enabled:         true,
		Image:           "background.jpg",
		Rotate:          "conversation",
		Dim:             0.55,
		Brightness:      1,
		ImageOpacity:    1,
		ImageBlur:       6,
		Glass:           0.5,
		Blur:            22,
		TerminalOpacity: 0.82,
		Position:        "center",
		Size:            "cover",
		Mode:            "dark",
		AutoClear:       true,
	}
}

// Rotating tells whether each conversation gets its own picture.
func (c Config) Rotating() bool { return c.Rotate != "off" }

var positionRE = regexp.MustCompile(`^[a-zA-Z0-9 .%-]{1,40}$`)

// Clean brings every value back into the range the loader accepts (the same
// clamps as NUM in src/loader.js).
func (c Config) Clean() Config {
	d := Defaults()
	clamp := func(v, lo, hi float64) float64 { return max(lo, min(hi, v)) }
	c.Dim = clamp(c.Dim, 0, 0.95)
	c.Brightness = clamp(c.Brightness, 0.3, 1.6)
	c.ImageOpacity = clamp(c.ImageOpacity, 0.1, 1)
	c.ImageBlur = clamp(c.ImageBlur, 0, 60)
	c.Glass = clamp(c.Glass, 0, 1)
	c.Blur = clamp(c.Blur, 0, 80)
	c.TerminalOpacity = clamp(c.TerminalOpacity, 0.3, 1)
	if c.Rotate != "off" {
		c.Rotate = "conversation"
	}
	if !positionRE.MatchString(c.Position) {
		c.Position = d.Position
	}
	if c.Size != "cover" && c.Size != "contain" {
		c.Size = d.Size
	}
	if c.Mode != "dark" && c.Mode != "light" && c.Mode != "auto" {
		c.Mode = d.Mode
	}
	if c.Image == "" {
		c.Image = d.Image
	}
	return c
}

// readRaw reads config.json as loose JSON, or an empty map.
func (b *Backdrop) readRaw() map[string]json.RawMessage {
	raw := map[string]json.RawMessage{}
	if data, err := os.ReadFile(b.path("config.json")); err == nil {
		if json.Unmarshal(data, &raw) != nil || raw == nil {
			raw = map[string]json.RawMessage{}
		}
	}
	return raw
}

// ReadConfig reads config.json over the defaults. It is forgiving: a field
// with the wrong type keeps its default instead of failing the whole file.
func (b *Backdrop) ReadConfig() Config {
	cfg := Defaults()
	raw := b.readRaw()
	fields := map[string]any{
		"enabled": &cfg.Enabled, "image": &cfg.Image, "rotate": &cfg.Rotate,
		"dim": &cfg.Dim, "brightness": &cfg.Brightness, "imageOpacity": &cfg.ImageOpacity,
		"imageBlur": &cfg.ImageBlur, "glass": &cfg.Glass, "blur": &cfg.Blur,
		"terminalOpacity": &cfg.TerminalOpacity, "position": &cfg.Position, "size": &cfg.Size,
		"mode": &cfg.Mode, "autoClear": &cfg.AutoClear, "refresh": &cfg.Refresh,
	}
	for key, target := range fields {
		if value, ok := raw[key]; ok {
			_ = json.Unmarshal(value, target) // a bad value leaves the default
		}
	}
	return cfg.Clean()
}

// WriteConfig saves config.json atomically, keeping the keys it does not know;
// Claude picks it up within ~2 s.
func (b *Backdrop) WriteConfig(cfg Config) error {
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return err
	}
	known, err := json.Marshal(cfg.Clean())
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(known, &fields); err != nil {
		return err
	}
	merged := b.readRaw()
	for key, value := range fields {
		merged[key] = value
	}
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(b.path("config.json"), append(data, '\n'))
}

// UpdateConfig reads, changes and writes config.json.
func (b *Backdrop) UpdateConfig(change func(*Config)) (Config, error) {
	cfg := b.ReadConfig()
	change(&cfg)
	return cfg.Clean(), b.WriteConfig(cfg)
}

func writeAtomic(file string, data []byte) error {
	tmp := file + ".claude-backdrop-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// ---------------------------------------------------------------- support folder

const customCSS = `/* Tes propres règles CSS pour Claude Desktop, rechargées en direct.
 * claude-backdrop n'écrase jamais ce fichier (theme.css, lui, est remplacé
 * à chaque mise à jour de l'outil).
 *
 * Exemples :
 *
 *   Recadrer l'image sur le haut du tableau :
 *     :root { --cb-position: 50% 20% !important; }
 *
 *   Barre latérale opaque, sans verre :
 *     .dframe-sidebar { background-color: #141414 !important; backdrop-filter: none !important; }
 *
 *   Déplacer le bouton galerie (en haut à droite) :
 *     #cb-gallery-btn { right: 200px !important; }
 *
 *   Un calque reste opaque ? « claude-backdrop status » le liste ; ajoute ici :
 *     .sa-classe { background: transparent !important; }
 */
`

// EnsureSupport creates the support folder and brings theme.css up to date
// (written only when it changed, so Claude does not reload for nothing).
func (b *Backdrop) EnsureSupport() error {
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		return err
	}
	theme := b.path("theme.css")
	if current, err := os.ReadFile(theme); err != nil || string(current) != payload.ThemeCSS {
		if err := writeAtomic(theme, []byte(payload.ThemeCSS)); err != nil {
			return err
		}
	}
	custom := b.path("custom.css")
	if _, err := os.Stat(custom); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(custom, []byte(customCSS), 0o644); err != nil {
			return err
		}
	}
	if _, err := os.Stat(b.path("config.json")); errors.Is(err, os.ErrNotExist) {
		return b.WriteConfig(Defaults())
	}
	return nil
}

// SetEnabled turns the theme on or off without touching the install.
func (b *Backdrop) SetEnabled(enabled bool) error {
	if err := b.EnsureSupport(); err != nil {
		return err
	}
	_, err := b.UpdateConfig(func(c *Config) { c.Enabled = enabled })
	return err
}

// FixedImagePath is the fixed picture on disk, or "" if missing.
func (b *Backdrop) FixedImagePath() string {
	name := b.ReadConfig().Image
	file := name
	if !filepath.IsAbs(name) {
		file = b.path(name)
	}
	if _, err := os.Stat(file); err != nil {
		return ""
	}
	return file
}
