package backdrop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/M-U-C-K-A/claude-code/internal/macos"
	payload "github.com/M-U-C-K-A/claude-code/src"
)

// LoaderState says whether Claude.app carries this release's loader.
type LoaderState int

const (
	LoaderMissing LoaderState = iota
	LoaderCurrent
	LoaderOutdated
)

type Backup struct {
	Version string
	Full    bool // a complete signed copy of Claude.app, not just app.asar
}

// Status gathers everything `status` and the TUI show.
type Status struct {
	AppFound   bool
	AppErr     error // Claude.app is there but could not be read
	App        string
	AppVersion string
	Running    bool
	LoaderTag  string
	Loader     LoaderState
	HashOK     bool
	Signature  *macos.Signature // nil unless asked for (slow)
	Backups    []Backup

	Dir        string
	Config     Config
	Paintings  []string // gallery ids on disk
	FixedImage string   // path, "" if missing
	Report     *Report  // what the loader last saw inside Claude
}

// Report is status.json, written by the loader from inside Claude.
type Report struct {
	Loader  string       `json:"loader"`
	At      time.Time    `json:"at"`
	App     string       `json:"app"`
	Enabled bool         `json:"enabled"`
	Image   string       `json:"image"`
	Pages   []PageReport `json:"pages"`
}

type PageReport struct {
	URL   string     `json:"url"`
	CSS   string     `json:"css"`
	Error string     `json:"error"`
	Page  *PageState `json:"page"`
}

// PageState is what the page script reports about one Claude window.
type PageState struct {
	Image     string  `json:"image"`
	Painting  string  `json:"painting"`
	Mode      string  `json:"mode"`
	Cleared   int     `json:"cleared"`
	Glass     int     `json:"glass"`
	Terminals int     `json:"terminals"`
	Opaque    []Layer `json:"opaque"`
}

// Layer is a large opaque surface the page script could not make see-through.
type Layer struct {
	Tag        string  `json:"tag"`
	ID         string  `json:"id"`
	Class      string  `json:"class"`
	Background string  `json:"background"`
	Share      float64 `json:"share"`
}

func (l Layer) Selector() string {
	s := l.Tag
	if l.ID != "" {
		s += "#" + l.ID
	}
	for _, c := range strings.Fields(l.Class) {
		s += "." + c
	}
	return s
}

// ReadReport reads status.json, or nil when the loader never wrote it.
func (b *Backdrop) ReadReport() *Report {
	data, err := os.ReadFile(b.path("status.json"))
	if err != nil {
		return nil
	}
	var report Report
	if json.Unmarshal(data, &report) != nil {
		return nil
	}
	return &report
}

// Status reads the state of Claude.app (the signature only when asked: it
// takes a few seconds) and of the support folder.
func (b *Backdrop) Status(withSignature bool) Status {
	st := Status{App: b.App, Dir: b.Dir, Config: b.ReadConfig(), FixedImage: b.FixedImagePath(), Report: b.ReadReport()}
	for _, p := range Gallery {
		if b.HasPainting(p.ID) {
			st.Paintings = append(st.Paintings, p.ID)
		}
	}
	if runtime.GOOS != "darwin" || b.requireApp() != nil {
		return st
	}
	st.AppFound = true
	state, err := b.ReadAppState(withSignature)
	if err != nil {
		st.AppErr = err
		return st
	}
	st.AppVersion = state.Version
	st.Running = macos.IsRunning(b.App)
	st.LoaderTag = state.Info.LoaderTag
	st.HashOK = state.HashOK
	if withSignature {
		st.Signature = &state.Signature
	}
	if loader, err := payload.BuildLoader(Version); err == nil && st.LoaderTag != "" {
		st.Loader = LoaderOutdated
		if loader.SameLoader(st.LoaderTag) {
			st.Loader = LoaderCurrent
		}
	}
	versions, _ := os.ReadDir(b.path("backups"))
	for _, v := range versions {
		_, err := os.Stat(filepath.Join(b.path("backups"), v.Name(), "Claude.app"))
		st.Backups = append(st.Backups, Backup{Version: v.Name(), Full: err == nil})
	}
	return st
}

// Rescan asks the loader, through config.json, to look at every Claude window
// again, and waits for its fresh report. Returns nil when Claude did not
// answer in time (closed, or loader not installed).
func (b *Backdrop) Rescan(timeout time.Duration) *Report {
	if _, err := os.Stat(b.path("config.json")); err != nil {
		return nil
	}
	before := time.Now().Truncate(time.Millisecond)
	if _, err := b.UpdateConfig(func(c *Config) { c.Refresh = before.UnixMilli() }); err != nil {
		return nil
	}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		report := b.ReadReport()
		if report == nil || report.At.Before(before) {
			continue
		}
		complete := true
		for _, page := range report.Pages {
			complete = complete && (page.Page != nil || page.Error != "")
		}
		if complete {
			return report
		}
	}
	return nil
}
