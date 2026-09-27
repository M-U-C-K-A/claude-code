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
	switch report.PageScript {
	case "support":
		add(row("script", sOK.Render("dossier de support")+sMuted.Render(" (appliqué en direct)")))
	case "builtin":
		add(row("script", sWarn.Render("intégré au loader")+sMuted.Render(" : relance claude-backdrop pour l'actualiser")))
	default:
		add(row("script", sWarn.Render("ancien loader")+sMuted.Render(" : fais « Mettre à jour » pour les correctifs en direct")))
	}
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
				size := "aperçu"
				if p.Full {
					size = "pleine résolution"
				}
				line += sMuted.Render(" [" + p.Painting + ", " + size + "]")
			}
			add(line)
			add("    " + sMuted.Render(fmt.Sprintf("%d calques transparents · %d en verre · %d terminaux", p.Cleared, p.Glass, p.Terminals)))
			for _, e := range p.Errors {
				add("    " + sErr.Render("erreur ") + e)
			}
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

// ProbeReport renders what covers each Claude page: the element stacks at a
// few points and the large layers, as the page script measured them. This is
// what to look at (or send) when a surface stays opaque.
func ProbeReport(st *backdrop.Status) string {
	if st.Report == nil {
		return ""
	}
	var out []string
	add := func(lines ...string) { out = append(out, lines...) }
	for _, page := range st.Report.Pages {
		if page.Page == nil || page.Page.Probe == nil {
			continue
		}
		p := page.Page.Probe
		add("", sTitle.Render("Relevé")+sMuted.Render("  "+page.URL))
		if p.Error != "" {
			add("  " + sErr.Render(p.Error))
			continue
		}
		if len(p.Picture) > 0 {
			var parts []string
			for _, k := range []string{"variable", "sheet", "shown", "html", "body"} {
				if v, ok := p.Picture[k]; ok {
					parts = append(parts, fmt.Sprintf("%s=%v", k, v))
				}
			}
			add("  image  " + sMuted.Render(strings.Join(parts, " · ")))
		}
		for _, name := range []string{"centre", "gauche", "droite"} {
			if stack, ok := p.Points[name]; ok {
				add("  " + sBlue.Render(name))
				for _, el := range stack {
					add("    " + probeLine(el))
				}
			}
		}
		if len(p.Layers) > 0 {
			add("  " + sBlue.Render("grands calques"))
			for _, el := range p.Layers {
				add("    " + probeLine(el))
			}
		}
	}
	return strings.Join(out, "\n")
}

func probeLine(el backdrop.ProbeEl) string {
	parts := []string{el.El}
	if len(el.Box) == 4 {
		parts = append(parts, sMuted.Render(fmt.Sprintf("%d×%d@%d,%d", el.Box[2], el.Box[3], el.Box[0], el.Box[1])))
	}
	for _, kv := range [][2]string{{"bg", el.Bg}, {"img", el.Img}, {"::before", el.Before}, {"::after", el.After},
		{"pe", el.PE}, {"pos", el.Pos}, {"opacity", el.Opacity}, {"role", el.Role}, {"cb", el.CB}} {
		if kv[1] != "" {
			parts = append(parts, sMuted.Render(kv[0]+" ")+kv[1])
		}
	}
	if el.Shadow {
		parts = append(parts, sMuted.Render("shadow"))
	}
	return strings.Join(parts, "  ")
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
