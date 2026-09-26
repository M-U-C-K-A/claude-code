// Package tui is the interactive side of claude-backdrop, built with Bubble
// Tea: pick a painting (with a preview), tune the rendering live, check what
// Claude shows, install or uninstall.
package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/M-U-C-K-A/claude-code/internal/backdrop"
)

// Run opens the interface. Any error on the way is shown inside it; the
// returned error is Bubble Tea's own.
func Run(b *backdrop.Backdrop) error {
	_, err := tea.NewProgram(newModel(b), tea.WithAltScreen()).Run()
	return err
}

type screen int

const (
	scrHome screen = iota
	scrPaintings
	scrSettings
	scrStatus
	scrConfirm
	scrTask
	scrInput
)

const (
	previewCols = 34
	minPreviewW = 100 // terminal width from which the preview fits beside the list
	rescanWait  = 6 * time.Second
)

// ---------------------------------------------------------------- messages

type statusMsg struct {
	st   backdrop.Status
	full bool
}
type rescanMsg struct{ report *backdrop.Report }
type thumbsMsg map[string]string
type lineMsg struct{ kind, text string }
type taskDoneMsg struct {
	result any
	err    error
}

// reporter streams progress lines of a background job to the interface.
type reporter chan<- tea.Msg

func (r reporter) Step(t string) { r <- lineMsg{kindStep, t} }
func (r reporter) OK(t string)   { r <- lineMsg{kindOK, t} }
func (r reporter) Warn(t string) { r <- lineMsg{kindWarn, t} }
func (r reporter) Note(t string) { r <- lineMsg{kindNote, t} }

// ---------------------------------------------------------------- model

type task struct {
	title    string
	lines    []lineMsg
	running  bool
	err      error
	done     string // closing message on success
	ch       chan tea.Msg
	blocking bool   // touching Claude.app: quitting now is refused
	autoBack bool   // on success, go straight back (with a flash)
	back     screen // where to go once read
	then     func(m *Model, result any) tea.Cmd
}

type confirmState struct {
	install *backdrop.InstallPlan
	restore *backdrop.RestorePlan
	yes     bool // the focused button
	purge   bool
}

type Model struct {
	b             *backdrop.Backdrop
	width, height int
	scr           screen
	cursor        map[screen]int

	cfg        backdrop.Config
	st         *backdrop.Status // nil while loading
	full       *backdrop.Status // with the signature, for the diagnostic
	loading    bool             // full status being read
	rescanning bool
	pointed    bool // the home cursor was placed after the first status

	thumbs  map[string]string
	flash   lineMsg
	spin    spinner.Model
	task    *task
	confirm *confirmState
	input   textinput.Model
}

func newModel(b *backdrop.Backdrop) *Model {
	m := &Model{b: b, cursor: map[screen]int{}, thumbs: map[string]string{}}
	m.spin = spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(sTitle))
	m.input = textinput.New()
	m.input.Prompt = sTitle.Render("› ")
	m.input.Placeholder = "~/Images/tableau.jpg ou https://…"
	m.input.CharLimit = 1024
	m.input.Width = 56
	// Refresh theme.css right away: a theme fix applies live, no reinstall.
	if err := b.EnsureSupport(); err != nil {
		m.flash = lineMsg{kindErr, err.Error()}
	}
	m.cfg = b.ReadConfig()
	return m
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.loadStatus(false), m.spin.Tick, tea.SetWindowTitle("Claude Backdrop"))
}

func (m *Model) loadStatus(full bool) tea.Cmd {
	if full {
		m.loading = true
	}
	return func() tea.Msg { return statusMsg{m.b.Status(full), full} }
}

func (m *Model) loadThumbs() tea.Cmd {
	if m.width < minPreviewW {
		return nil
	}
	b := m.b
	fixed := b.FixedImagePath()
	return func() tea.Msg {
		out := thumbsMsg{}
		for _, p := range backdrop.Gallery {
			if b.HasPainting(p.ID) {
				out[p.ID] = thumbnail(b.PaintingFile(p.ID), previewCols)
			}
		}
		if fixed != "" {
			out["fixed"] = thumbnail(fixed, previewCols)
		}
		return out
	}
}

func (m *Model) busy() bool {
	return m.st == nil || m.loading || m.rescanning || (m.task != nil && m.task.running)
}

func listen(ch <-chan tea.Msg) tea.Cmd { return func() tea.Msg { return <-ch } }

// run starts a background job whose progress shows on the task screen.
func (m *Model) run(t *task, job func(r backdrop.Reporter) (any, error)) tea.Cmd {
	t.running = true
	t.ch = make(chan tea.Msg, 256)
	m.task, m.scr, m.flash = t, scrTask, lineMsg{}
	go func(ch chan tea.Msg) {
		result, err := job(reporter(ch))
		ch <- taskDoneMsg{result, err}
	}(t.ch)
	return tea.Batch(listen(t.ch), m.spin.Tick)
}

// ---------------------------------------------------------------- update

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		grew := m.width < minPreviewW && msg.Width >= minPreviewW
		m.width, m.height = msg.Width, msg.Height
		if grew {
			return m, m.loadThumbs()
		}
		return m, nil

	case spinner.TickMsg:
		if !m.busy() {
			return m, nil // let the tick loop stop
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case statusMsg:
		st := msg.st
		m.st = &st
		m.cfg = st.Config
		if msg.full {
			m.full, m.loading = &st, false
			// Opening the diagnostic asks Claude for a fresh look, like `status`.
			if m.scr == scrStatus && st.Running && st.Loader != backdrop.LoaderMissing {
				return m, m.rescan()
			}
		} else if !m.pointed && st.AppFound && st.Loader == backdrop.LoaderMissing && m.scr == scrHome {
			m.cursor[scrHome] = homeIndex("install") // first run: point at what to do
		}
		m.pointed = true
		return m, nil

	case rescanMsg:
		m.rescanning = false
		if msg.report == nil {
			m.flash = lineMsg{kindWarn, "Pas de réponse de Claude en 6 s : est-il ouvert, avec le thème installé ?"}
		} else if m.full != nil {
			m.full.Report = msg.report
		}
		return m, nil

	case thumbsMsg:
		m.thumbs = msg
		return m, nil

	case lineMsg:
		if m.task != nil {
			m.task.lines = append(m.task.lines, msg)
			return m, listen(m.task.ch)
		}
		return m, nil

	case taskDoneMsg:
		t := m.task
		t.running, t.err = false, msg.err
		m.cfg = m.b.ReadConfig()
		cmds := []tea.Cmd{m.loadStatus(false), m.loadThumbs()}
		switch {
		case msg.err != nil:
		case t.then != nil:
			cmds = append(cmds, t.then(m, msg.result))
		case t.autoBack && !hasWarning(t.lines):
			m.scr, m.flash = t.back, lastOK(t.lines)
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		return m.key(msg)
	}

	if m.scr == scrInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func hasWarning(lines []lineMsg) bool {
	for _, l := range lines {
		if l.kind == kindWarn {
			return true
		}
	}
	return false
}

func lastOK(lines []lineMsg) lineMsg {
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].kind == kindOK {
			return lines[i]
		}
	}
	return lineMsg{}
}

func (m *Model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		if m.task != nil && m.task.running && m.task.blocking {
			m.flash = lineMsg{kindWarn, "Claude.app est en cours de modification : patiente jusqu'à la fin."}
			return m, nil
		}
		return m, tea.Quit
	}
	if m.scr != scrInput && m.scr != scrSettings {
		m.flash = lineMsg{}
	}
	switch m.scr {
	case scrHome:
		return m.keyHome(k)
	case scrPaintings:
		return m.keyPaintings(k)
	case scrSettings:
		return m.keySettings(k)
	case scrStatus:
		switch k {
		case "r":
			if !m.rescanning && !m.loading {
				return m, m.rescan()
			}
		case "esc", "q", "backspace", "left", "h", "enter":
			m.scr = scrHome
		}
	case scrConfirm:
		return m.keyConfirm(k)
	case scrTask:
		if !m.task.running && (k == "enter" || k == "esc" || k == "q" || k == " ") {
			m.scr = m.task.back
		}
	case scrInput:
		switch k {
		case "esc":
			m.scr = scrPaintings
			return m, nil
		case "enter":
			spec := cleanDroppedPath(m.input.Value())
			if spec == "" {
				return m, nil
			}
			return m, m.run(&task{title: "Image", autoBack: true, back: scrPaintings}, func(r backdrop.Reporter) (any, error) {
				return nil, m.b.SetFixedImage(spec, r)
			})
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// cleanDroppedPath undoes the quoting a terminal adds when a file is dropped
// on it ('/My Pictures/a.jpg' or /My\ Pictures/a.jpg).
func cleanDroppedPath(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	if strings.Contains(s, "://") {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// move handles ↑/↓ on a list of n entries.
func (m *Model) move(k string, n int) bool {
	c := m.cursor[m.scr]
	switch k {
	case "up", "k":
		c = (c - 1 + n) % n
	case "down", "j", "tab":
		c = (c + 1) % n
	case "home", "g":
		c = 0
	case "end", "G":
		c = n - 1
	default:
		return false
	}
	m.cursor[m.scr] = c
	return true
}

func (m *Model) rescan() tea.Cmd {
	m.rescanning = true
	b := m.b
	return tea.Batch(m.spin.Tick, func() tea.Msg { return rescanMsg{b.Rescan(rescanWait)} })
}

// ---------------------------------------------------------------- home

var homeKeys = []string{"painting", "settings", "theme", "status", "install", "uninstall", "quit"}

func homeIndex(key string) int {
	for i, k := range homeKeys {
		if k == key {
			return i
		}
	}
	return 0
}

func (m *Model) homeEntry(key string) (label, detail string) {
	switch key {
	case "painting":
		var present []string
		if m.st != nil {
			present = m.st.Paintings
		}
		return "Tableau", PictureSummary(m.cfg, present)
	case "settings":
		return "Réglages", SettingsSummary(m.cfg)
	case "theme":
		if m.cfg.Enabled {
			return "Thème", sOK.Render("activé") + sMuted.Render("  · entrée pour le couper")
		}
		return "Thème", sWarn.Render("coupé") + sMuted.Render("  · entrée pour le remettre")
	case "status":
		return "Diagnostic", "ce que Claude affiche vraiment, calques restés opaques"
	case "install":
		switch {
		case m.st == nil:
			return "Installer", "…"
		case !m.st.AppFound || m.st.AppErr != nil:
			return "Installer", LoaderBadge(m.st)
		case m.st.Loader == backdrop.LoaderCurrent:
			return "Réinstaller", sOK.Render("à jour") + sMuted.Render(" · rien à faire")
		case m.st.Loader == backdrop.LoaderOutdated:
			return "Mettre à jour", sWarn.Render("nouvelle version du loader")
		default:
			return "Installer", sWarn.Render("pose le thème dans Claude.app")
		}
	case "uninstall":
		return "Désinstaller", "remet Claude d'origine"
	case "quit":
		return "Quitter", ""
	}
	return key, ""
}

func (m *Model) keyHome(k string) (tea.Model, tea.Cmd) {
	if m.move(k, len(homeKeys)) {
		return m, nil
	}
	switch k {
	case "q", "esc":
		return m, tea.Quit
	case "enter", " ", "right", "l":
	default:
		return m, nil
	}
	switch homeKeys[m.cursor[scrHome]] {
	case "painting":
		m.scr = scrPaintings
		return m, m.loadThumbs()
	case "settings":
		m.scr = scrSettings
	case "theme":
		enabled := !m.cfg.Enabled
		if err := m.b.SetEnabled(enabled); err != nil {
			m.flash = lineMsg{kindErr, err.Error()}
		} else if enabled {
			m.flash = lineMsg{kindOK, "Thème remis, Claude le recharge tout seul."}
		} else {
			m.flash = lineMsg{kindOK, "Thème coupé (le loader reste installé)."}
		}
		m.cfg = m.b.ReadConfig()
	case "status":
		m.scr = scrStatus
		if !m.loading {
			return m, tea.Batch(m.loadStatus(true), m.spin.Tick)
		}
	case "install":
		return m, m.run(&task{title: "Installation", back: scrHome, then: (*Model).afterPrepareInstall},
			func(r backdrop.Reporter) (any, error) { return m.b.PrepareInstall(backdrop.InstallOptions{}, r) })
	case "uninstall":
		return m, m.run(&task{title: "Désinstallation", back: scrHome, then: (*Model).afterPrepareRestore},
			func(r backdrop.Reporter) (any, error) { return m.b.PrepareRestore(r) })
	case "quit":
		return m, tea.Quit
	}
	return m, nil
}

func (m *Model) afterPrepareInstall(result any) tea.Cmd {
	plan := result.(*backdrop.InstallPlan)
	if plan.UpToDate {
		m.task.done = "Le loader est déjà installé et à jour : le thème se recharge tout seul, rien à redémarrer."
		return nil
	}
	m.confirm, m.scr = &confirmState{install: plan, yes: true}, scrConfirm
	return nil
}

func (m *Model) afterPrepareRestore(result any) tea.Cmd {
	plan := result.(*backdrop.RestorePlan)
	if plan.AlreadyOriginal {
		m.task.done = fmt.Sprintf("Claude %s est déjà d'origine, rien à restaurer.", plan.AppVersion)
		return nil
	}
	m.confirm, m.scr = &confirmState{restore: plan}, scrConfirm
	return nil
}

func (m *Model) keyConfirm(k string) (tea.Model, tea.Cmd) {
	c := m.confirm
	switch k {
	case "left", "right", "h", "l", "tab":
		c.yes = !c.yes
	case "p", " ":
		if c.restore != nil {
			c.purge = !c.purge
		}
	case "esc", "q", "n":
		m.scr = scrHome
	case "o", "y":
		c.yes = true
		return m.keyConfirm("enter")
	case "enter":
		if !c.yes {
			m.scr = scrHome
			return m, nil
		}
		if c.install != nil {
			plan := c.install
			return m, m.run(&task{title: "Installation", blocking: true, back: scrHome,
				done: "C'est fait. Choisis un tableau ou règle le rendu : tout s'applique en direct."},
				func(r backdrop.Reporter) (any, error) { return nil, plan.Apply(r) })
		}
		plan, purge := c.restore, c.purge
		return m, m.run(&task{title: "Désinstallation", blocking: true, back: scrHome,
			done: "Claude est revenu à son état d'origine."},
			func(r backdrop.Reporter) (any, error) { return nil, plan.Apply(purge, false, r) })
	}
	return m, nil
}

// ---------------------------------------------------------------- paintings

// Painting list: "random", the gallery, then "custom".
func paintingKeys() []string {
	keys := []string{"random"}
	for _, p := range backdrop.Gallery {
		keys = append(keys, p.ID)
	}
	return append(keys, "custom")
}

func (m *Model) keyPaintings(k string) (tea.Model, tea.Cmd) {
	keys := paintingKeys()
	if m.move(k, len(keys)) {
		return m, nil
	}
	switch k {
	case "esc", "q", "backspace", "left", "h":
		m.scr = scrHome
		return m, nil
	case "enter", " ", "right", "l":
	default:
		return m, nil
	}
	switch key := keys[m.cursor[scrPaintings]]; key {
	case "random":
		return m, m.run(&task{title: "Tableau", autoBack: true, back: scrPaintings}, func(r backdrop.Reporter) (any, error) {
			return nil, m.b.SetRotation(r)
		})
	case "custom":
		m.scr = scrInput
		m.input.SetValue("")
		return m, m.input.Focus()
	default:
		return m, m.run(&task{title: "Tableau", autoBack: true, back: scrPaintings}, func(r backdrop.Reporter) (any, error) {
			return nil, m.b.SetFixedImage(key, r)
		})
	}
}

// ---------------------------------------------------------------- settings

type setting struct {
	name, help string
	// sliders
	get          func(c backdrop.Config) float64
	set          func(c *backdrop.Config, v float64)
	lo, hi, step float64
	format       func(float64) string
	// choices
	choices []string
	labels  map[string]string
	getS    func(c backdrop.Config) string
	setS    func(c *backdrop.Config, v string)
}

var settings = []setting{
	{name: "Voile", help: "assombrit le tableau pour que le texte reste lisible",
		get: func(c backdrop.Config) float64 { return c.Dim }, set: func(c *backdrop.Config, v float64) { c.Dim = v },
		lo: 0, hi: 0.95, step: 0.05, format: percent},
	{name: "Flou du tableau", help: "floute l'image de fond",
		get: func(c backdrop.Config) float64 { return c.ImageBlur }, set: func(c *backdrop.Config, v float64) { c.ImageBlur = v },
		lo: 0, hi: 60, step: 1, format: pixels},
	{name: "Verre", help: "opacité des panneaux en verre dépoli (barre latérale, terminal)",
		get: func(c backdrop.Config) float64 { return c.Glass }, set: func(c *backdrop.Config, v float64) { c.Glass = v },
		lo: 0, hi: 1, step: 0.05, format: percent},
	{name: "Flou du verre", help: "flou derrière les panneaux (0 = verre net)",
		get: func(c backdrop.Config) float64 { return c.Blur }, set: func(c *backdrop.Config, v float64) { c.Blur = v },
		lo: 0, hi: 80, step: 2, format: pixels},
	{name: "Cadrage", help: "quelle partie du tableau reste visible",
		choices: []string{"center", "top", "bottom"}, labels: positionLabels,
		getS: func(c backdrop.Config) string { return c.Position }, setS: func(c *backdrop.Config, v string) { c.Position = v }},
	{name: "Taille", help: "remplir la fenêtre (en rognant) ou montrer tout le tableau",
		choices: []string{"cover", "contain"}, labels: sizeLabels,
		getS: func(c backdrop.Config) string { return c.Size }, setS: func(c *backdrop.Config, v string) { c.Size = v }},
	{name: "Tableaux au hasard", help: "lesquels tirer au sort : les sombres, le clair (L'École d'Athènes), ou selon le mode de Claude",
		choices: []string{"dark", "light", "auto"}, labels: modeLabels,
		getS: func(c backdrop.Config) string { return c.Mode }, setS: func(c *backdrop.Config, v string) { c.Mode = v }},
	{name: "Calques opaques", help: "détecte et rend transparents les fonds que le thème ne connaît pas",
		choices: []string{"on", "off"}, labels: map[string]string{"on": "rendus transparents", "off": "laissés tels quels"},
		getS: func(c backdrop.Config) string {
			if c.AutoClear {
				return "on"
			}
			return "off"
		},
		setS: func(c *backdrop.Config, v string) { c.AutoClear = v == "on" }},
}

func (m *Model) keySettings(k string) (tea.Model, tea.Cmd) {
	if m.move(k, len(settings)) {
		return m, nil
	}
	s := settings[m.cursor[scrSettings]]
	dir := 0
	switch k {
	case "esc", "q", "backspace":
		m.scr, m.flash = scrHome, lineMsg{}
		return m, nil
	case "right", "l", "+", "=", "enter", " ":
		dir = 1
	case "left", "h", "-":
		dir = -1
	case "shift+right", "L":
		dir = 5
	case "shift+left", "H":
		dir = -5
	case "d":
		defaults := backdrop.Defaults()
		m.apply(func(c *backdrop.Config) {
			if s.get != nil {
				s.set(c, s.get(defaults))
			} else {
				s.setS(c, s.getS(defaults))
			}
		})
		return m, nil
	default:
		return m, nil
	}
	m.apply(func(c *backdrop.Config) {
		if s.get != nil {
			v := math.Round(s.get(*c)/s.step+float64(dir)) * s.step
			s.set(c, max(s.lo, min(s.hi, math.Round(v*1000)/1000)))
			return
		}
		i := 0
		for j, choice := range s.choices {
			if choice == s.getS(*c) {
				i = j
			}
		}
		n := len(s.choices)
		s.setS(c, s.choices[((i+dir)%n+n)%n])
	})
	return m, nil
}

func (m *Model) apply(change func(*backdrop.Config)) {
	cfg, err := m.b.UpdateConfig(change)
	if err != nil {
		m.flash = lineMsg{kindErr, err.Error()}
		return
	}
	m.cfg = cfg
	m.flash = lineMsg{kindOK, "appliqué en direct dans Claude"}
}

// ---------------------------------------------------------------- view

func (m *Model) View() string {
	var body, keys string
	switch m.scr {
	case scrHome:
		body, keys = m.viewHome(), "↑↓ choisir · entrée ouvrir · q quitter"
	case scrPaintings:
		body, keys = m.viewPaintings(), "↑↓ choisir · entrée appliquer · échap retour"
	case scrSettings:
		body, keys = m.viewSettings(), "↑↓ choisir · ←→ régler (maj : ×5) · d défaut · échap retour"
	case scrStatus:
		body, keys = m.viewStatus(), "r relancer l'analyse · échap retour"
	case scrConfirm:
		body, keys = m.viewConfirm(), "←→ choisir · entrée valider · échap annuler"
		if m.confirm.restore != nil {
			keys = "←→ choisir · espace effacer aussi les réglages · entrée valider · échap annuler"
		}
	case scrTask:
		body = m.viewTask()
		if !m.task.running {
			keys = "entrée continuer"
		}
	case scrInput:
		body, keys = m.viewInput(), "entrée valider · échap retour"
	}
	parts := []string{m.viewHeader(), "", body}
	if m.flash.text != "" {
		parts = append(parts, "", RenderLine(m.flash.kind, m.flash.text))
	}
	if keys != "" {
		parts = append(parts, "", sMuted.Render(keys))
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(parts, "\n"))
}

func (m *Model) viewHeader() string {
	title := sTitle.Render("◆ Claude Backdrop") + sMuted.Render("  "+backdrop.Version)
	var chips []string
	if m.st == nil {
		chips = append(chips, m.spin.View()+sMuted.Render(" lecture de Claude…"))
	} else {
		if m.st.AppVersion != "" {
			chips = append(chips, sText.Render("Claude "+m.st.AppVersion))
		}
		chips = append(chips, LoaderBadge(m.st))
		if !m.cfg.Enabled {
			chips = append(chips, sWarn.Render("thème coupé"))
		}
	}
	return title + "\n" + strings.Join(chips, sFaint.Render(" · "))
}

// entry renders one list line: cursor, label, detail.
func entry(selected bool, label string, width int, detail string) string {
	cursor, style := "  ", sText
	if selected {
		cursor, style = sTitle.Render("› "), sSel
	}
	return cursor + style.Render(fmt.Sprintf("%-*s", width, label)) + "  " + sMuted.Render(detail)
}

func (m *Model) viewHome() string {
	var lines []string
	for i, key := range homeKeys {
		label, detail := m.homeEntry(key)
		if key == "quit" {
			lines = append(lines, "")
		}
		lines = append(lines, entry(i == m.cursor[scrHome], label, 14, detail))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewPaintings() string {
	keys := paintingKeys()
	cur := keys[m.cursor[scrPaintings]]
	var lines []string
	lines = append(lines, sTitle.Render("Tableau de fond"), "")
	for i, key := range keys {
		selected := i == m.cursor[scrPaintings]
		mark := "  "
		var label, detail string
		switch key {
		case "random":
			label, detail = "Au hasard", "un tableau différent par conversation"
			if m.cfg.Rotating() {
				mark = sOK.Render("✓ ")
			}
		case "custom":
			label, detail = "Mon image…", "un fichier ou une URL"
			if !m.cfg.Rotating() && m.cfg.ImageSource != "" {
				if _, ok := backdrop.PaintingByID(m.cfg.ImageSource); !ok {
					mark, detail = sOK.Render("✓ "), m.cfg.ImageSource
				}
			}
		default:
			p, _ := backdrop.PaintingByID(key)
			label = p.Title
			tone := sMuted.Render("sombre")
			if p.Mode == "light" {
				tone = sBlue.Render("clair ")
			}
			state := sFaint.Render("○")
			if m.b.HasPainting(key) {
				state = sOK.Render("●")
			}
			detail = fmt.Sprintf("%s %s  %s", state, tone, fmt.Sprintf("%s, %d", p.Artist, p.Year))
			if !m.cfg.Rotating() && m.cfg.ImageSource == key {
				mark = sOK.Render("✓ ")
			}
		}
		if key == "custom" {
			lines = append(lines, "")
		}
		lines = append(lines, mark+entry(selected, label, 24, detail))
	}
	lines = append(lines, "", sMuted.Render("● téléchargé  ○ téléchargé au premier choix"))
	list := strings.Join(lines, "\n")
	if m.width < minPreviewW {
		return list
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, list, "    ", m.preview(cur))
}

func (m *Model) preview(key string) string {
	var art, caption string
	switch key {
	case "random":
		caption = "Chaque conversation tire un\ntableau parmi les " + labelOf(modeLabels, m.cfg.Mode) + ",\net le garde si tu recharges."
	case "custom":
		if !m.cfg.Rotating() {
			art = m.thumbs["fixed"]
		}
		caption = "JPEG, PNG, HEIC… converti et\nredimensionné automatiquement."
	default:
		p, _ := backdrop.PaintingByID(key)
		art = m.thumbs[key]
		caption = sText.Render(p.Title) + "\n" + fmt.Sprintf("%s, %d", p.Artist, p.Year)
		if art == "" && !m.b.HasPainting(key) {
			caption += "\n\npas encore téléchargé :\nentrée pour le télécharger"
		}
		if p.Mode == "light" && m.cfg.Mode == "dark" {
			caption += "\n\ntableau clair : hors du tirage au\nsort tant que Réglages › Tableaux\nau hasard = sombres"
		}
	}
	parts := []string{}
	if art != "" {
		parts = append(parts, art)
	}
	parts = append(parts, sMuted.Render(caption))
	return sFrame.Width(previewCols + 2).Render(strings.Join(parts, "\n"))
}

func slider(v, lo, hi float64, width int) string {
	filled := int((v-lo)/(hi-lo)*float64(width) + 0.5)
	filled = max(0, min(width, filled))
	return sTitle.Render(strings.Repeat("━", filled)) + sFaint.Render(strings.Repeat("─", width-filled))
}

func (m *Model) viewSettings() string {
	lines := []string{sTitle.Render("Réglages") + sMuted.Render("  appliqués en direct dans Claude"), ""}
	for i, s := range settings {
		selected := i == m.cursor[scrSettings]
		var value string
		if s.get != nil {
			v := s.get(m.cfg)
			value = slider(v, s.lo, s.hi, 24) + "  " + sText.Render(fmt.Sprintf("%6s", s.format(v)))
		} else {
			text := labelOf(s.labels, s.getS(m.cfg))
			if selected {
				value = sTitle.Render("‹ ") + sSel.Render(text) + sTitle.Render(" ›")
			} else {
				value = sText.Render("  " + text)
			}
		}
		cursor, style := "  ", sText
		if selected {
			cursor, style = sTitle.Render("› "), sSel
		}
		lines = append(lines, cursor+style.Render(fmt.Sprintf("%-19s", s.name))+" "+value)
	}
	lines = append(lines, "", sMuted.Render(settings[m.cursor[scrSettings]].help))
	return strings.Join(lines, "\n")
}

func (m *Model) viewStatus() string {
	head := sTitle.Render("Diagnostic")
	switch {
	case m.loading:
		head += "  " + m.spin.View() + sMuted.Render(" lecture de Claude.app et de sa signature…")
	case m.rescanning:
		head += "  " + m.spin.View() + sMuted.Render(" nouvelle analyse demandée à Claude…")
	}
	st := m.full
	if st == nil {
		st = m.st
	}
	if st == nil {
		return head
	}
	return head + "\n\n" + StatusReport(st)
}

func (m *Model) viewTask() string {
	t := m.task
	lines := []string{sTitle.Render(t.title), ""}
	for _, l := range t.lines {
		lines = append(lines, RenderLine(l.kind, l.text))
	}
	gap := func() {
		if len(t.lines) > 0 {
			lines = append(lines, "")
		}
	}
	switch {
	case t.running:
		lines = append(lines, m.spin.View())
	case t.err != nil:
		gap()
		lines = append(lines, RenderLine(kindErr, t.err.Error()))
	case t.done != "":
		gap()
		lines = append(lines, RenderLine(kindOK, t.done))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) viewConfirm() string {
	c := m.confirm
	var title string
	var text []string
	yes := "Installer"
	if c.install != nil {
		title = "Installer dans Claude " + c.install.AppVersion
		text = []string{
			"Claude va être fermé, son app.asar reçoit le loader, puis l'app est",
			"re-signée localement (ad-hoc) et relancée.",
			"",
			sWarn.Render("Tant que le thème est posé, les mises à jour automatiques de Claude"),
			sWarn.Render("échouent : pour mettre Claude à jour, désinstalle, mets à jour, réinstalle."),
		}
	} else {
		yes = "Désinstaller"
		title = "Remettre Claude " + c.restore.AppVersion + " d'origine"
		if c.restore.FullBackup {
			text = []string{"Claude va être fermé et remplacé par la copie faite à l'installation,",
				"signature d'Anthropic comprise : les mises à jour automatiques remarchent."}
		} else {
			text = []string{"Claude va être fermé et le loader retiré. Pas de copie complète : la",
				"signature reste locale (réinstalle Claude depuis claude.ai/download pour",
				"retrouver celle d'Anthropic)."}
		}
		box := "[ ]"
		if c.purge {
			box = sTitle.Render("[×]")
		}
		text = append(text, "", box+" effacer aussi réglages, images et sauvegardes")
	}
	lines := []string{sTitle.Render(title), ""}
	if m.task != nil {
		for _, l := range m.task.lines {
			lines = append(lines, RenderLine(l.kind, l.text))
		}
		lines = append(lines, "")
	}
	lines = append(lines, text...)
	yesB, noB := sGhost.Render(yes), sGhost.Render("Annuler")
	if c.yes {
		yesB = sButton.Render(yes)
	} else {
		noB = sButton.Render("Annuler")
	}
	lines = append(lines, "", yesB+"  "+noB)
	return strings.Join(lines, "\n")
}

func (m *Model) viewInput() string {
	return strings.Join([]string{
		sTitle.Render("Mon image"),
		"",
		sText.Render("Chemin d'un fichier (glisse-le dans le terminal) ou adresse web :"),
		"",
		m.input.View(),
	}, "\n")
}
