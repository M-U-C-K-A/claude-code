package backdrop

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/M-U-C-K-A/claude-code/internal/asar"
	"github.com/M-U-C-K-A/claude-code/internal/macos"
	payload "github.com/M-U-C-K-A/claude-code/src"
)

// AppState is what Claude.app looks like right now.
type AppState struct {
	Paths     macos.Paths
	Archive   []byte
	Info      asar.Info
	Plists    []macos.PlistHash
	Signature macos.Signature // zero unless read with the signature
	Version   string
	HashOK    bool // every Info.plist pins the current header hash
}

func (b *Backdrop) requireApp() error {
	if _, err := os.Stat(macos.PathsOf(b.App).Asar); err != nil {
		return userErrorf("Claude Desktop introuvable dans %s.\nInstalle-le depuis https://claude.ai/download, ou indique son chemin avec --app.", b.App)
	}
	return nil
}

// ReadAppState inspects Claude.app. The signature check takes a few seconds,
// so it is optional.
func (b *Backdrop) ReadAppState(withSignature bool) (*AppState, error) {
	paths := macos.PathsOf(b.App)
	archive, err := os.ReadFile(paths.Asar)
	if err != nil {
		return nil, err
	}
	info, err := asar.Inspect(archive)
	if err != nil {
		return nil, fmt.Errorf("app.asar : %w", err)
	}
	version, err := macos.AppVersion(b.App)
	if err != nil {
		return nil, err
	}
	state := &AppState{Paths: paths, Archive: archive, Info: info, Plists: macos.IntegrityPlists(b.App), Version: version}
	state.HashOK = len(state.Plists) > 0
	for _, p := range state.Plists {
		state.HashOK = state.HashOK && p.Hash == info.HeaderHash
	}
	if withSignature {
		state.Signature = macos.SignatureOf(b.App)
	}
	return state, nil
}

func checkWritable(dir string) error {
	probe := filepath.Join(dir, fmt.Sprintf(".claude-backdrop-%d", os.Getpid()))
	err := os.WriteFile(probe, nil, 0o600)
	if err == nil {
		return os.Remove(probe)
	}
	if errors.Is(err, syscall.EPERM) {
		return userErrorf("macOS empêche ce terminal de modifier les applications.\n" +
			"  Réglages Système › Confidentialité et sécurité › Gestion des apps (App Management)\n" +
			"  → active ton terminal (Terminal, iTerm, Ghostty…), puis relance.")
	}
	if errors.Is(err, syscall.EACCES) {
		return userErrorf("%s ne t'appartient pas (Claude installé par un administrateur ?).\n  Donne-toi les droits : sudo chown -R \"$(whoami)\" %q", dir, filepath.Dir(filepath.Dir(dir)))
	}
	return err
}

func (b *Backdrop) backupDir(version string) string { return b.path("backups", version) }

func bundleSizeMB(app string) int64 {
	var total int64
	_ = filepath.WalkDir(app, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total / 1024 / 1024
}

// backup keeps two layers per Claude version:
//   - the original app.asar (small, always), enough to take the loader out;
//   - a full copy of Claude.app while it still has Anthropic's signature, so
//     restoring brings back the exact original (auto-updates included).
func (b *Backdrop) backup(state *AppState, noAppBackup bool, r Reporter) (string, error) {
	dir := b.backupDir(state.Version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	asarCopy := filepath.Join(dir, "app.asar")
	if _, err := os.Stat(asarCopy); errors.Is(err, fs.ErrNotExist) {
		original := state.Archive
		if state.Info.LoaderTag != "" {
			unpatched, err := asar.Unpatch(state.Archive)
			if err != nil {
				return "", err
			}
			original = unpatched.Archive
		}
		if err := writeAtomic(asarCopy, original); err != nil {
			return "", err
		}
	}
	full := filepath.Join(dir, "Claude.app")
	if _, err := os.Stat(full); !noAppBackup && errors.Is(err, fs.ErrNotExist) {
		if state.Signature.Kind == "developer-id" && state.Signature.Valid {
			r.Step(fmt.Sprintf("Copie de sauvegarde de Claude.app (~%d Mo)", bundleSizeMB(b.App)))
			partial := full + ".partial"
			os.RemoveAll(partial)
			if err := macos.CopyBundle(b.App, partial); err != nil {
				return "", err
			}
			if err := os.Rename(partial, full); err != nil {
				return "", err
			}
		} else {
			r.Warn("Claude.app n'a plus sa signature d'origine : pas de copie complète (seul app.asar est sauvegardé).")
		}
	}
	// Keep only the backup of the installed version.
	others, _ := os.ReadDir(filepath.Dir(dir))
	for _, other := range others {
		if other.Name() != state.Version {
			os.RemoveAll(filepath.Join(filepath.Dir(dir), other.Name()))
		}
	}
	return dir, nil
}

// replaceBundle swaps in a full copy of the bundle: copy next to the app, then
// two renames.
func replaceBundle(app, source string) error {
	parent := filepath.Dir(app)
	incoming := filepath.Join(parent, ".Claude.app.claude-backdrop-incoming")
	outgoing := filepath.Join(parent, ".Claude.app.claude-backdrop-outgoing")
	os.RemoveAll(incoming)
	os.RemoveAll(outgoing)
	if err := macos.CopyBundle(source, incoming); err != nil {
		return err
	}
	if err := os.Rename(app, outgoing); err != nil {
		return err
	}
	if err := os.Rename(incoming, app); err != nil {
		return err
	}
	return os.RemoveAll(outgoing)
}

// ---------------------------------------------------------------- install

type InstallOptions struct {
	Image       string // a picture to set right away (gallery id, file, URL)
	NoAppBackup bool
	NoLaunch    bool
}

// InstallPlan is an install checked and ready to apply: everything that can
// fail without touching Claude has been done.
type InstallPlan struct {
	b       *Backdrop
	opts    InstallOptions
	state   *AppState
	loader  payload.Loader
	patched asar.Result

	UpToDate   bool   // nothing to do: the loader is already in place
	AppVersion string // Claude's version
	Files      int    // files checked in the patched archive
}

// PrepareInstall sets up the theme files and the pictures, then dry-runs the
// patch in memory. Claude is not touched.
func (b *Backdrop) PrepareInstall(opts InstallOptions, r Reporter) (*InstallPlan, error) {
	if err := requireMac(); err != nil {
		return nil, err
	}
	if err := b.requireApp(); err != nil {
		return nil, err
	}
	loader, err := payload.BuildLoader(Version)
	if err != nil {
		return nil, err
	}
	r.Step("Lecture de Claude.app")
	state, err := b.ReadAppState(true)
	if err != nil {
		return nil, err
	}
	plan := &InstallPlan{b: b, opts: opts, state: state, loader: loader, AppVersion: state.Version}

	r.Step("Fichiers du thème")
	if err := b.EnsureSupport(); err != nil {
		return nil, err
	}
	if opts.Image != "" {
		if err := b.ChooseImage(opts.Image, r); err != nil {
			return nil, err
		}
	}
	available, err := b.EnsureGallery(r)
	if err != nil {
		return nil, err
	}
	switch {
	case !b.ReadConfig().Rotating() && b.FixedImagePath() == "":
		if err := b.SetFixed("socrates", r); err != nil {
			r.Warn("Image par défaut indisponible : le thème marche quand même, choisis une image ensuite.")
		}
	case available == 0:
		r.Warn("Aucun tableau téléchargé : le thème marche, mais sans image. Réessaie plus tard depuis « Image ».")
	default:
		r.OK(fmt.Sprintf("%d images dans la galerie", available))
	}
	r.OK("Thème prêt dans " + b.Dir)

	if len(state.Plists) == 0 {
		return nil, userErrorf("Aucun ElectronAsarIntegrity dans les Info.plist de Claude : structure inattendue, je n'y touche pas.")
	}
	if loader.SameLoader(state.Info.LoaderTag) && state.HashOK && state.Signature.Valid {
		plan.UpToDate = true
		return plan, nil
	}

	r.Step("Essai du patch sur une copie en mémoire")
	plan.patched, err = asar.Patch(state.Archive, loader.Tag, loader.Code)
	if err != nil {
		return nil, err
	}
	if plan.Files, err = asar.Verify(plan.patched.Archive); err != nil {
		return nil, err
	}
	if info, err := asar.Inspect(plan.patched.Archive); err != nil || info.LoaderTag != loader.Tag {
		return nil, errors.New("le loader n'apparaît pas dans l'archive patchée")
	}
	r.OK(fmt.Sprintf("Archive valide (%d fichiers vérifiés, point d'entrée %s)", plan.Files, plan.patched.MainPath))

	if macos.RunningInside(b.App) {
		return nil, userErrorf("Ce terminal tourne DANS Claude : le fermer le tuerait en plein milieu.\n  Lance claude-backdrop depuis Terminal.app, iTerm, Ghostty…")
	}
	if err := checkWritable(state.Paths.Resources); err != nil {
		return nil, err
	}
	return plan, nil
}

// Apply closes Claude, writes the loader into app.asar, updates the integrity
// hashes, re-signs and relaunches. Any failure rolls back.
func (p *InstallPlan) Apply(r Reporter) error {
	if p.UpToDate {
		return nil
	}
	b, state := p.b, p.state
	dir, err := b.backup(state, p.opts.NoAppBackup, r)
	if err != nil {
		return err
	}
	r.OK("Sauvegarde : " + dir)

	r.Step("Fermeture de Claude")
	wasRunning, err := macos.Quit(b.App)
	if err != nil {
		return err
	}
	type savedPlist struct {
		path string
		data []byte
	}
	var originals []savedPlist
	for _, entry := range state.Plists {
		data, err := os.ReadFile(entry.Plist)
		if err != nil {
			return err
		}
		originals = append(originals, savedPlist{entry.Plist, data})
	}
	apply := func() error {
		if p.patched.Changed {
			r.Step("Installation du loader dans app.asar")
			if err := writeAtomic(state.Paths.Asar, p.patched.Archive); err != nil {
				return err
			}
		}
		r.Step("Mise à jour de l'empreinte d'intégrité (Info.plist)")
		for _, entry := range state.Plists {
			if err := macos.SetIntegrity(entry.Plist, p.patched.HeaderHash); err != nil {
				return err
			}
		}
		r.Step("Signature locale (ad-hoc)")
		return macos.Resign(b.App, payload.Entitlements)
	}
	if err := apply(); err != nil {
		r.Warn("Échec : " + err.Error())
		r.Step("Retour à l'état précédent")
		full := filepath.Join(b.backupDir(state.Version), "Claude.app")
		if _, statErr := os.Stat(full); statErr == nil {
			if rbErr := replaceBundle(b.App, full); rbErr == nil {
				r.OK("Claude.app d'origine remis en place.")
				return userErrorf("Installation annulée, Claude est revenu à son état précédent.")
			}
		}
		rollback := writeAtomic(state.Paths.Asar, state.Archive)
		for _, saved := range originals {
			if rollback == nil {
				rollback = os.WriteFile(saved.path, saved.data, 0o644)
			}
		}
		if rollback == nil && !macos.SignatureOf(b.App).Valid {
			rollback = macos.Resign(b.App, payload.Entitlements)
		}
		if rollback != nil {
			r.Warn(fmt.Sprintf("Retour arrière incomplet (%v). Réinstalle Claude depuis https://claude.ai/download.", rollback))
		} else {
			r.OK("app.asar et Info.plist d'origine remis en place.")
		}
		return userErrorf("Installation annulée, Claude est revenu à son état précédent.")
	}
	r.OK("Loader installé, intégrité et signature à jour.")
	if !p.opts.NoLaunch {
		if wasRunning {
			r.Step("Relance de Claude")
		} else {
			r.Step("Ouverture de Claude")
		}
		macos.Launch(b.App)
	}
	r.Note("Au premier lancement, macOS peut redemander le trousseau (« Claude Safe Storage » → Toujours autoriser)")
	r.Note("et les autorisations micro / écran : c'est la nouvelle signature locale, une seule fois.")
	return nil
}

// ---------------------------------------------------------------- restore

// RestorePlan is a restore checked and ready to apply.
type RestorePlan struct {
	b     *Backdrop
	state *AppState

	AlreadyOriginal bool // Claude is untouched: nothing to put back
	FullBackup      bool // a signed copy of Claude.app is available
	AppVersion      string
}

// PrepareRestore looks at what can be put back.
func (b *Backdrop) PrepareRestore(r Reporter) (*RestorePlan, error) {
	if err := requireMac(); err != nil {
		return nil, err
	}
	if err := b.requireApp(); err != nil {
		return nil, err
	}
	r.Step("Lecture de Claude.app")
	state, err := b.ReadAppState(true)
	if err != nil {
		return nil, err
	}
	plan := &RestorePlan{b: b, state: state, AppVersion: state.Version}
	plan.AlreadyOriginal = state.Info.LoaderTag == "" && state.Signature.Kind == "developer-id" && state.Signature.Valid
	if plan.AlreadyOriginal {
		return plan, nil
	}
	full := filepath.Join(b.backupDir(state.Version), "Claude.app")
	if _, err := os.Stat(full); err == nil {
		r.Step("Vérification de la copie d'origine")
		plan.FullBackup = macos.SignatureOf(full).Valid
	}
	if macos.RunningInside(b.App) {
		return nil, userErrorf("Ce terminal tourne DANS Claude : lance claude-backdrop depuis Terminal.app, iTerm, Ghostty…")
	}
	if err := checkWritable(state.Paths.Resources); err != nil {
		return nil, err
	}
	return plan, nil
}

// Apply puts Claude back the way it was. With purge, the support folder
// (settings, pictures, backups) goes too.
func (p *RestorePlan) Apply(purge, noLaunch bool, r Reporter) error {
	b, state := p.b, p.state
	if p.AlreadyOriginal {
		r.OK(fmt.Sprintf("Claude %s est déjà d'origine, rien à restaurer.", state.Version))
	} else {
		r.Step("Fermeture de Claude")
		if _, err := macos.Quit(b.App); err != nil {
			return err
		}
		dir := b.backupDir(state.Version)
		if p.FullBackup {
			r.Step("Remise en place de la copie d'origine de Claude.app")
			if err := replaceBundle(b.App, filepath.Join(dir, "Claude.app")); err != nil {
				return err
			}
			r.OK("Claude d'origine restauré, signature d'Anthropic comprise : les mises à jour automatiques remarchent.")
		} else {
			r.Step("Retrait du loader")
			original, err := os.ReadFile(filepath.Join(dir, "app.asar"))
			if err != nil {
				unpatched, err := asar.Unpatch(state.Archive)
				if err != nil {
					return err
				}
				original = unpatched.Archive
			}
			if err := writeAtomic(state.Paths.Asar, original); err != nil {
				return err
			}
			info, err := asar.Inspect(original)
			if err != nil {
				return err
			}
			for _, entry := range state.Plists {
				if err := macos.SetIntegrity(entry.Plist, info.HeaderHash); err != nil {
					return err
				}
			}
			if err := macos.Resign(b.App, payload.Entitlements); err != nil {
				return err
			}
			r.OK("Loader retiré.")
			r.Warn("Sans copie complète, la signature reste locale : pour retrouver celle d'Anthropic (et les mises à jour automatiques), réinstalle Claude depuis https://claude.ai/download.")
		}
		if !noLaunch {
			macos.Launch(b.App)
		}
	}
	if purge {
		if err := os.RemoveAll(b.Dir); err != nil {
			return err
		}
		r.OK("Dossier " + b.Dir + " supprimé.")
	}
	return nil
}
