package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/M-U-C-K-A/claude-code/internal/backdrop"
)

// Ayu palette: Ayu Dark on dark terminals, Ayu Light on light ones.
var (
	cGold   = lipgloss.AdaptiveColor{Light: "#E59400", Dark: "#E6B450"}
	cFg     = lipgloss.AdaptiveColor{Light: "#5C6166", Dark: "#BFBDB6"}
	cMuted  = lipgloss.AdaptiveColor{Light: "#8A9199", Dark: "#6C7380"}
	cFaint  = lipgloss.AdaptiveColor{Light: "#D8D8D7", Dark: "#2D3240"}
	cBlue   = lipgloss.AdaptiveColor{Light: "#399EE6", Dark: "#59C2FF"}
	cGreen  = lipgloss.AdaptiveColor{Light: "#86B300", Dark: "#AAD94C"}
	cRed    = lipgloss.AdaptiveColor{Light: "#E65050", Dark: "#D95757"}
	cOrange = lipgloss.AdaptiveColor{Light: "#FA8D3E", Dark: "#FF8F40"}
)

var (
	sTitle  = lipgloss.NewStyle().Foreground(cGold).Bold(true)
	sText   = lipgloss.NewStyle().Foreground(cFg)
	sSel    = lipgloss.NewStyle().Foreground(cGold).Bold(true)
	sMuted  = lipgloss.NewStyle().Foreground(cMuted)
	sFaint  = lipgloss.NewStyle().Foreground(cFaint)
	sBlue   = lipgloss.NewStyle().Foreground(cBlue)
	sOK     = lipgloss.NewStyle().Foreground(cGreen)
	sWarn   = lipgloss.NewStyle().Foreground(cOrange)
	sErr    = lipgloss.NewStyle().Foreground(cRed)
	sKey    = lipgloss.NewStyle().Foreground(cFg).Bold(true)
	sButton = lipgloss.NewStyle().Foreground(lipgloss.Color("#0B0E14")).Background(cGold).Bold(true).Padding(0, 2)
	sGhost  = lipgloss.NewStyle().Foreground(cMuted).Padding(0, 2)
	sFrame  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(0, 1)
)

// Line kinds shared by the CLI printer and the TUI task log.
const (
	kindStep = "step"
	kindOK   = "ok"
	kindWarn = "warn"
	kindNote = "note"
	kindErr  = "err"
)

// RenderLine formats one progress line: a symbol, then the text; continuation
// lines are indented under it.
func RenderLine(kind, text string) string {
	var symbol string
	style := sText
	switch kind {
	case kindStep:
		symbol = sTitle.Render("›")
	case kindOK:
		symbol = sOK.Render("✓")
	case kindWarn:
		symbol = sWarn.Render("!")
	case kindErr:
		symbol = sErr.Render("✗")
		style = sErr
	default:
		symbol = " "
		style = sMuted
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = style.Render(l)
	}
	return symbol + " " + strings.Join(lines, "\n  ")
}

func percent(v float64) string { return fmt.Sprintf("%d %%", int(v*100+0.5)) }
func pixels(v float64) string  { return fmt.Sprintf("%d px", int(v+0.5)) }

var positionLabels = map[string]string{"center": "centre", "top": "haut", "bottom": "bas"}
var sizeLabels = map[string]string{"cover": "remplit la fenêtre", "contain": "image entière"}

func labelOf(labels map[string]string, value string) string {
	if l, ok := labels[value]; ok {
		return l
	}
	return value
}

// SettingsSummary is the one-line recap of the rendering settings.
func SettingsSummary(c backdrop.Config) string {
	return fmt.Sprintf("voile %s · luminosité %s · flou %s · verre %s",
		percent(c.Dim), percent(c.Brightness), pixels(c.ImageBlur), percent(c.Glass))
}
