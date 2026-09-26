// Package payload holds what claude-backdrop puts into Claude Desktop, embedded
// in the binary: the loader appended to app.asar (loader.js), the page script
// it injects (page.js), the Ayu Dark theme (theme.css), and the entitlements
// used to re-sign the app.
package payload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	_ "embed"
)

//go:embed loader.js
var loaderTemplate string

//go:embed page.js
var PageScript string

//go:embed theme.css
var ThemeCSS string

//go:embed entitlements.plist
var Entitlements []byte

// Loader is the code appended to Claude's main script.
type Loader struct {
	// Tag is written in the asar markers: "<version>-<hash>".
	Tag string
	// Hash covers loader.js and page.js: the same hash means the same loader,
	// whatever release of claude-backdrop installed it.
	Hash string
	Code string
}

// BuildLoader inlines the tag and the page script into loader.js.
func BuildLoader(version string) (Loader, error) {
	sum := sha256.Sum256([]byte(loaderTemplate + "\x00" + PageScript))
	hash := hex.EncodeToString(sum[:])[:10]
	tag := version + "-" + hash
	code := strings.Replace(loaderTemplate, "const VERSION = __CB_VERSION__;", "const VERSION = "+jsString(tag)+";", 1)
	code = strings.Replace(code, "const PAGE_SCRIPT = __CB_PAGE_SCRIPT__;", "const PAGE_SCRIPT = "+jsString(PageScript)+";", 1)
	if strings.Contains(code, "__CB_") {
		return Loader{}, errors.New("loader template still has unfilled placeholders")
	}
	return Loader{Tag: tag, Hash: hash, Code: code}, nil
}

// SameLoader tells whether an installed tag carries this loader, even when an
// older release of claude-backdrop wrote it (the version prefix differs, the
// hash does not).
func (l Loader) SameLoader(installedTag string) bool {
	return installedTag == l.Tag || strings.HasSuffix(installedTag, "-"+l.Hash)
}

// jsString is a JSON string literal, which is also a valid JS string literal.
func jsString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}
