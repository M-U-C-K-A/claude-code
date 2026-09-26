package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/M-U-C-K-A/claude-code/internal/backdrop"
)

func row(name, value string) string {
	return "  " + sMuted.Render(fmt.Sprintf("%-12s", name)) + " " + value
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// LoaderBadge says in a few words whether Claude carries the theme.
func LoaderBadge(st *backdrop.Status) string {
	switch {
	case !st.AppFound && runtime.GOOS != "darwin":
		return sMuted.Render("macOS uniquement")
	case !st.AppFound:
		return sWarn.Render("Claude Desktop introuvable")
	case st.AppErr != nil:
		return sErr.Render("Claude.app illisible")
	case st.Loader == backdrop.LoaderCurrent:
		return sOK.Render("thème installé")
	case st.Loader == backdrop.LoaderOutdated:
		return sWarn.Render("loader à mettre à jour")
	default:
		return sWarn.Render("thème pas encore installé")
	}
}

// StatusReport renders the full status: Claude.app, the theme files, and what
// the loader last reported from inside Claude.
func StatusReport(st *backdrop.Status) string {
	var out []string
	add := func(lines ...string) { out = append(out, lines...) }

	add(sTitle.Render("Claude Desktop"))
	switch {
	case !st.AppFound:
		add(row("état", LoaderBadge(st)+sMuted.Render("  "+st.App)))
	case st.AppErr != nil:
		add(row("état", sErr.Render(st.AppErr.Error())))
	default:
		running := sMuted.Render("fermé")
		if st.Running {
			running = sOK.Render("ouvert")
		}
		add(row("version", st.AppVersion+"  "+running))
		add(row("chemin", sMuted.Render(st.App)))
		loader := LoaderBadge(st)
		if st.LoaderTag != "" {
			loader += sMuted.Render("  " + st.LoaderTag)
		}
		add(row("loader", loader))
		if st.HashOK {
			add(row("intégrité", sOK.Render("ok")))
		} else {
			add(row("intégrité", sErr.Render("Info.plist ne correspond pas à app.asar")))
		}
		if sig := st.Signature; sig != nil {
			text := map[string]string{"developer-id": "Anthropic (d'origine)", "adhoc": "locale (ad-hoc)"}[sig.Kind]
			if text == "" {
				text = sig.Kind
			}
			if sig.Valid {
				add(row("signature", text))
			} else {
				add(row("signature", text+sErr.Render("  invalide")))
			}
		}
		var backups []string
		for _, bk := range st.Backups {
			if bk.Full {
				backups = append(backups, bk.Version+" (app complète)")
			} else {
				backups = append(backups, bk.Version)
			}
		}
		if len(backups) == 0 {
			backups = []string{sMuted.Render("aucune")}
		}
		add(row("sauvegardes", strings.Join(backups, ", ")))
	}

	add("", sTitle.Render("Thème"))
	add(row("dossier", sMuted.Render(tildePath(st.Dir))))
	enabled := sOK.Render("activé")
	if !st.Config.Enabled {
		enabled = sWarn.Render("désactivé")
	}
	add(row("état", enabled))
	add(row("image", st.Picture))
	add(row("réglages", SettingsSummary(st.Config)))

	report := st.Report
	if report == nil {
		add("", sTitle.Render("Dans Claude"))
		add("  " + sMuted.Render("pas encore de rapport : ouvre Claude avec le thème installé"))
		return strings.Join(out, "\n")
	}
	age := time.Since(report.At).Round(time.Second)
	add("", sTitle.Render("Dans Claude")+sMuted.Render(fmt.Sprintf("  rapport du loader il y a %s", humanAge(age))))
	blocked, opaque := false, false
	for _, page := range report.Pages {
		css := sOK.Render("ok")
		if page.CSS != "inserted" {
			css = sErr.Render(page.CSS)
		}
		line := fmt.Sprintf("  %s %s  %s %s", sTitle.Render("•"), page.URL, sMuted.Render("css"), css)
		if p := page.Page; p != nil {
			image := map[string]string{
				"data": sOK.Render("affichée"), "blob": sOK.Render("affichée (blob)"), "url": sOK.Render("affichée"),
				"blocked": sErr.Render("bloquée par la CSP"), "none": sWarn.Render("aucune"),
			}[p.Image]
			if image == "" {
				image = p.Image
			}
			line += sMuted.Render("  image ") + image
			if p.Painting != "" && p.Painting != "none" {
				line += sMuted.Render(" [" + p.Painting + "]")
			}
			add(line)
			add("    " + sMuted.Render(fmt.Sprintf("%d calques transparents · %d en verre · %d terminaux", p.Cleared, p.Glass, p.Terminals)))
			for i, layer := range p.Opaque {
				if i == 6 {
					break
				}
				add("    " + sWarn.Render("opaque ") + layer.Selector() + sMuted.Render(fmt.Sprintf("  %s, %d %% de la fenêtre", layer.Background, int(layer.Share*100+0.5))))
			}
			blocked = blocked || p.Image == "blocked"
			opaque = opaque || len(p.Opaque) > 0
		} else {
			add(line)
		}
		if page.Error != "" {
			add("    " + sErr.Render(page.Error))
		}
	}
	if blocked {
		add("", RenderLine(kindWarn, "L'image est refusée par la politique de sécurité (CSP) de la page : ouvre une issue avec ce rapport."))
	}
	if opaque {
		add("", RenderLine(kindNote, "Calques opaques restants : rends-les transparents dans "+tildePath(filepath.Join(st.Dir, "custom.css"))))
	}
	return strings.Join(out, "\n")
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	default:
		return fmt.Sprintf("%d jours", int(d.Hours()/24))
	}
}
