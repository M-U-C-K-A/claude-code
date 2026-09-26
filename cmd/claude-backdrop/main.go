// Command claude-backdrop puts a painting behind Claude Desktop (macOS), with
// the rest of the interface repainted in Ayu Dark. Without arguments it opens
// the interactive interface; a few plain commands cover scripting.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"

	"github.com/M-U-C-K-A/claude-code/internal/asar"
	"github.com/M-U-C-K-A/claude-code/internal/backdrop"
	"github.com/M-U-C-K-A/claude-code/internal/macos"
	"github.com/M-U-C-K-A/claude-code/internal/tui"
	payload "github.com/M-U-C-K-A/claude-code/src"
)

var (
	bold  = lipgloss.NewStyle().Bold(true)
	gold  = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#E59400", Dark: "#E6B450"}).Bold(true)
	muted = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8A9199", Dark: "#6C7380"})
)

const help = `%s — un tableau derrière Claude Desktop, thème Ayu Dark (macOS)

  %s                      ouvre l'interface : tableau, réglages, installation

  %s              installe (ou met à jour) le thème dans Claude
  %s            remet Claude d'origine   %s
  %s        %s
  %s             active / coupe le thème, sans rien désinstaller
  %s               état de l'installation et de ce que Claude affiche

  Options : --yes (pas de confirmation), --app <chemin de Claude.app>
  Fichiers : %s`

func usage(b *backdrop.Backdrop) string {
	ids := []string{"hasard"}
	for _, p := range backdrop.Gallery {
		ids = append(ids, p.ID)
	}
	return fmt.Sprintf(help,
		gold.Render("claude-backdrop"),
		bold.Render("claude-backdrop"),
		bold.Render("claude-backdrop install"),
		bold.Render("claude-backdrop uninstall"), muted.Render("(--purge : efface aussi réglages et images)"),
		bold.Render("claude-backdrop image <choix>"), strings.Join(ids, " | ")+" | fichier | url",
		bold.Render("claude-backdrop on | off"),
		bold.Render("claude-backdrop status"),
		muted.Render(b.Dir))
}

type options struct {
	args        []string
	yes         bool
	purge       bool
	noLaunch    bool
	noAppBackup bool
	help        bool
	version     bool
	app         string
	image       string
	asarFile    string
}

func parseArgs(argv []string) (options, error) {
	var o options
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--yes", "-y":
			o.yes = true
		case "--purge":
			o.purge = true
		case "--no-launch":
			o.noLaunch = true
		case "--no-app-backup":
			o.noAppBackup = true
		case "--help", "-h":
			o.help = true
		case "--version", "-v":
			o.version = true
		case "--app", "--image", "--asar":
			if i+1 >= len(argv) {
				return o, fmt.Errorf("%s attend une valeur", arg)
			}
			i++
			switch arg {
			case "--app":
				o.app = argv[i]
			case "--image":
				o.image = argv[i]
			default:
				o.asarFile = argv[i]
			}
		default:
			if strings.HasPrefix(arg, "--") {
				return o, fmt.Errorf("option inconnue : %s", arg)
			}
			o.args = append(o.args, arg)
		}
	}
	return o, nil
}

// printer shows progress lines on the terminal.
type printer struct{}

func (printer) Step(t string) { fmt.Println(tui.RenderLine("step", t)) }
func (printer) OK(t string)   { fmt.Println(tui.RenderLine("ok", t)) }
func (printer) Warn(t string) { fmt.Println(tui.RenderLine("warn", t)) }
func (printer) Note(t string) { fmt.Println(tui.RenderLine("note", t)) }

var out printer

func confirm(question string, o options) error {
	if o.yes {
		return nil
	}
	if !term.IsTerminal(os.Stdin.Fd()) {
		return errors.New("confirmation impossible hors terminal : relance avec --yes")
	}
	fmt.Printf("%s %s ", question, muted.Render("[o/N]"))
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "o", "oui", "y", "yes":
		return nil
	}
	return errors.New("annulé, rien n'a été modifié")
}

func main() {
	o, err := parseArgs(os.Args[1:])
	b := backdrop.New(o.app)
	if err == nil {
		err = run(b, o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, tui.RenderLine("err", err.Error()))
		os.Exit(1)
	}
}

func run(b *backdrop.Backdrop, o options) error {
	command := ""
	if len(o.args) > 0 {
		command = o.args[0]
	}
	switch {
	case o.version || command == "version":
		fmt.Println("claude-backdrop " + backdrop.Version)
		return nil
	case o.help || command == "help":
		fmt.Println(usage(b))
		return nil
	}
	switch command {
	case "":
		if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
			fmt.Println(usage(b))
			return nil
		}
		return tui.Run(b)
	case "install":
		return install(b, o)
	case "uninstall", "restore":
		return uninstall(b, o)
	case "image":
		if len(o.args) < 2 {
			return fmt.Errorf("quelle image ? claude-backdrop image <hasard | %s | fichier | url>", backdrop.Gallery[0].ID)
		}
		if err := b.ChooseImage(strings.Join(o.args[1:], " "), out); err != nil {
			return err
		}
		fmt.Println(muted.Render("  Claude recharge l'image tout seul (quelques secondes)."))
		return nil
	case "on", "off":
		if err := b.SetEnabled(command == "on"); err != nil {
			return err
		}
		if command == "on" {
			out.OK("Thème activé.")
		} else {
			out.OK("Thème coupé (le loader reste installé ; « claude-backdrop on » pour le remettre).")
		}
		return nil
	case "status", "doctor":
		return status(b)
	case "selftest":
		return selftest(b, o)
	case "gallery":
		return errors.New("« gallery » est devenu « claude-backdrop image hasard » (ou le menu Tableau de l'interface)")
	case "set":
		return errors.New("les réglages sont dans l'interface : lance « claude-backdrop » puis Réglages")
	}
	return fmt.Errorf("commande inconnue : %s\n\n%s", command, usage(b))
}

func install(b *backdrop.Backdrop, o options) error {
	fmt.Println(gold.Render("Claude Backdrop "+backdrop.Version) + muted.Render("  "+b.App))
	plan, err := b.PrepareInstall(backdrop.InstallOptions{Image: o.image, NoAppBackup: o.noAppBackup, NoLaunch: o.noLaunch}, out)
	if err != nil {
		return err
	}
	if plan.UpToDate {
		out.OK("Le loader est déjà installé et à jour : le thème se recharge tout seul, rien à redémarrer.")
		return nil
	}
	fmt.Println()
	fmt.Println("Claude va être fermé, son app.asar reçoit le loader, puis l'app est re-signée localement (ad-hoc).")
	fmt.Println(muted.Render("Tant que le thème est installé, les mises à jour automatiques de Claude échouent : voir le README."))
	if err := confirm("Continuer ?", o); err != nil {
		return err
	}
	if err := plan.Apply(out); err != nil {
		return err
	}
	fmt.Println()
	out.OK(bold.Render("C'est fait."))
	fmt.Println("  Pour choisir un tableau ou régler le rendu : " + bold.Render("claude-backdrop"))
	return nil
}

func uninstall(b *backdrop.Backdrop, o options) error {
	plan, err := b.PrepareRestore(out)
	if err != nil {
		return err
	}
	if !plan.AlreadyOriginal {
		if err := confirm(fmt.Sprintf("Claude va être fermé et remis dans son état d'origine (%s). Continuer ?", plan.AppVersion), o); err != nil {
			return err
		}
	}
	return plan.Apply(o.purge, o.noLaunch, out)
}

func status(b *backdrop.Backdrop) error {
	if err := b.EnsureSupport(); err != nil {
		return err
	}
	st := b.Status(true)
	if st.Running && st.Loader != backdrop.LoaderMissing {
		out.Step("Nouvelle analyse demandée à Claude…")
		if report := b.Rescan(6 * time.Second); report != nil {
			st.Report = report
		} else {
			out.Warn("Pas de réponse de Claude en 6 s : le rapport ci-dessous est l'ancien.")
		}
	}
	fmt.Println(gold.Render("Claude Backdrop " + backdrop.Version))
	fmt.Println()
	fmt.Println(tui.StatusReport(&st))
	return nil
}

// selftest tries the patch on a copy of an archive (Claude's by default)
// without writing anything: useful on a new Claude release.
func selftest(b *backdrop.Backdrop, o options) error {
	file := o.asarFile
	if file == "" {
		file = macos.PathsOf(b.App).Asar
	}
	file, _ = filepath.Abs(backdrop.ExpandHome(file))
	archive, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("archive introuvable : %s", file)
	}
	loader, err := payload.BuildLoader(backdrop.Version)
	if err != nil {
		return err
	}
	before, err := asar.Inspect(archive)
	if err != nil {
		return err
	}
	patched, err := asar.Patch(archive, loader.Tag, loader.Code)
	if err != nil {
		return err
	}
	files, err := asar.Verify(patched.Archive)
	if err != nil {
		return err
	}
	after, err := asar.Inspect(patched.Archive)
	if err != nil || after.LoaderTag != loader.Tag {
		return errors.New("le loader n'apparaît pas après patch")
	}
	if again, err := asar.Patch(patched.Archive, loader.Tag, loader.Code); err != nil || again.Changed {
		return errors.New("le patch n'est pas idempotent")
	}
	restored, err := asar.Unpatch(patched.Archive)
	if err != nil {
		return err
	}
	if info, _ := asar.Inspect(restored.Archive); info.LoaderTag != "" {
		return errors.New("le loader ne s'enlève pas")
	}
	if before.LoaderTag == "" && !bytes.Equal(restored.Archive, archive) {
		return errors.New("l'archive restaurée diffère de l'originale")
	}
	out.OK("selftest ok — " + file)
	fmt.Printf("  point d'entrée %s, %d fichiers vérifiés, loader %s\n", after.MainPath, files, loader.Tag)
	fmt.Printf("  empreinte d'en-tête : %s… → %s…\n", before.HeaderHash[:16], after.HeaderHash[:16])
	return nil
}
