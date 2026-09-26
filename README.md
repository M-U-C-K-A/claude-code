# claude-backdrop

Une **image de fond** derrière Claude Desktop sur **macOS**, avec le reste de
l'interface repeint en **Ayu Dark** : barre latérale et champ de saisie en verre
dépoli, texte et accents aux couleurs d'Ayu. Inspiré de la maquette « opencode »
sur *La Mort de Socrate* de David.

> ⚠️ **Non officiel.** Cet outil modifie les fichiers internes de Claude Desktop
> et le re-signe localement. Ça va probablement à l'encontre des conditions
> d'utilisation d'Anthropic, et ça casse la mise à jour automatique tant que le
> thème est posé (voir [Mises à jour](#mises-à-jour)). À utiliser en connaissance
> de cause.

## Aperçu

`preview.html` (à la racine) est une maquette autonome : ouvre-la dans un
navigateur pour voir le rendu Ayu Dark sur le tableau, sans rien installer.

## Prérequis

- macOS avec [Claude Desktop](https://claude.ai/download) dans `/Applications`
- [Go](https://go.dev/dl) 1.24 ou plus : `brew install go`
- La première fois, macOS peut demander d'autoriser ton terminal dans
  **Réglages Système › Confidentialité et sécurité › Gestion des apps**.

## Installation

```bash
git clone https://github.com/M-U-C-K-A/claude-code.git
cd claude-code
./claude-backdrop
```

Le premier lancement compile l'outil (quelques secondes), puis ouvre
l'interface. Choisis **Installer** : l'outil télécharge les tableaux (domaine
public), sauvegarde Claude.app, pose le loader, re-signe et relance Claude. Au
premier lancement, macOS peut redemander le trousseau (« Claude Safe Storage »
→ *Toujours autoriser*) et les autorisations micro/écran : c'est la nouvelle
signature locale, une seule fois.

Pour avoir la commande partout :
`ln -s "$PWD/claude-backdrop" /usr/local/bin/claude-backdrop`.

## L'interface

`claude-backdrop` sans argument ouvre une interface dans le terminal
(flèches, entrée, échap) :

- **Tableau** — un tableau au hasard par conversation, ou un seul fixe : un de
  la galerie (avec un aperçu dans le terminal), ou ton image, fichier ou URL
  (glisse le fichier dans le terminal).
- **Réglages** — voile, flou du tableau, verre, flou du verre, cadrage…, avec
  les flèches ←/→. Claude applique chaque changement en direct.
- **Thème** — le couper ou le remettre, sans rien désinstaller.
- **Diagnostic** — l'état de Claude.app et ce que le thème voit dans Claude :
  image affichée ou non, calques restés opaques.
- **Installer / Mettre à jour**, **Désinstaller**.

## En ligne de commande

Pour les scripts, ou si tu préfères :

```bash
claude-backdrop install             # installe (ou met à jour) le thème dans Claude
claude-backdrop uninstall           # remet Claude d'origine (--purge : efface aussi réglages et images)
claude-backdrop image hasard        # un tableau au hasard par conversation
claude-backdrop image pandemonium   # un tableau fixe : socrates, horatii, pandemonium, school-of-athens
claude-backdrop image ~/Images/x.jpg   # ton image (fichier ou URL)
claude-backdrop on | off            # active / coupe le thème
claude-backdrop status              # état de l'installation et de ce que Claude affiche
```

`--yes` saute la confirmation, `--app <chemin>` vise un autre Claude.app.

### Galerie

Par défaut, **chaque conversation reçoit un tableau au hasard**, stable pour
cette conversation (un rechargement garde le même). Le tirage se fait parmi les
tableaux **sombres** (*La Mort de Socrate*, *Le Serment des Horaces*,
*Pandémonium*) ; *L'École d'Athènes*, plus claire, n'entre dans le tirage que
si Réglages › Tableaux au hasard vaut « clairs » ou « selon le mode de
Claude ». Tous sont du domaine public, téléchargés depuis Wikimedia Commons.

Tout se recharge en direct. Tes propres règles CSS vont dans
`~/Library/Application Support/ClaudeBackdrop/custom.css` (jamais écrasé) ;
`theme.css` du même dossier est, lui, remplacé à chaque mise à jour de l'outil.

## Réglages

| Réglage (interface)  | Clé de `config.json` | Défaut         | Rôle |
|----------------------|----------------------|----------------|------|
| Voile                | `dim`                | `0.55`         | assombrissement du tableau (0 à 0.95) |
| Flou du tableau      | `imageBlur`          | `6`            | flou de l'image de fond en px (0 à 60) |
| Verre                | `glass`              | `0.5`          | opacité du verre (barre latérale, panneaux, terminal), 0 à 1 |
| Flou du verre        | `blur`               | `22`           | flou du verre en px (0 = sans flou) |
| Cadrage              | `position`           | `center`       | `center`, `top`, `bottom` (ou `50% 20%` à la main) |
| Taille               | `size`               | `cover`        | `cover` (remplit) ou `contain` (tableau entier) |
| Tableaux au hasard   | `mode`               | `dark`         | `dark`, `light` ou `auto` (suit le thème de Claude) |
| Calques opaques      | `autoClear`          | `true`         | détection automatique des calques opaques |
| Tableau              | `rotate`             | `conversation` | `conversation` = au hasard ; `off` = image fixe |

## Mises à jour

Tant que le thème est posé, la signature d'Anthropic est remplacée par une
signature locale, et l'updater de Claude **refuse d'installer** les mises à jour
(il vérifie la signature). Le plus simple pour mettre Claude à jour :

```bash
claude-backdrop uninstall   # remet le vrai Claude signé Anthropic → l'auto-update remarche
# … laisse Claude se mettre à jour (menu Claude › Rechercher les mises à jour), puis :
claude-backdrop install     # repose le thème sur la nouvelle version
```

La désinstallation remet exactement le Claude.app d'origine à partir de la
copie faite à l'installation (signature comprise), donc rien n'est abîmé. Après
une mise à jour subie, l'installation détecte la nouvelle version et repose
tout proprement.

Pour mettre **l'outil** à jour : `git pull`, puis `./claude-backdrop` (il se
recompile tout seul). Le thème (`theme.css`) se met à jour en direct ; le loader
n'est réinstallé que s'il a changé.

## Désinstaller

```bash
claude-backdrop uninstall            # Claude revient à l'original
claude-backdrop uninstall --purge    # + supprime réglages, images et sauvegardes
```

Si la copie complète manque (installé avec `--no-app-backup`), la
désinstallation retire quand même le loader ; pour retrouver la signature
d'Anthropic, réinstalle Claude depuis [claude.ai/download](https://claude.ai/download).

## Comment ça marche

Claude Desktop refuse les options de débogage : impossible d'injecter du style
de l'extérieur. La seule voie sur macOS aujourd'hui :

1. un petit **loader** est ajouté à la fin du script principal dans
   `Claude.app/Contents/Resources/app.asar` ;
2. l'**empreinte d'intégrité** (`ElectronAsarIntegrity`) est recalculée dans les
   `Info.plist` du bundle ;
3. l'app est **re-signée localement** (ad-hoc), avec les entitlements dont Claude
   a besoin (micro, caméra, virtualisation pour Cowork).

Le loader lui-même ne change jamais : il lit tout depuis
`~/Library/Application Support/ClaudeBackdrop/` et **recharge en direct** dès que
tu changes l'image ou un réglage — pas besoin de redémarrer Claude. Le thème
s'applique avec `webContents.insertCSS(..., { cssOrigin: "user" })`. Dans cette
origine, une règle `!important` passe devant toutes celles de l'app, même
`!important` : c'est pourquoi chaque token de couleur de Claude y est redéfini
en `!important` (une déclaration normale y perdrait toujours). Un script de page
rend en plus transparents les calques opaques que le CSS ne connaît pas par leur
nom.

## Sécurité et limites

- La signature d'Anthropic (et son *hardened runtime*) est remplacée par une
  signature locale : l'app est moins protégée contre l'injection de code.
- Les mises à jour automatiques échouent tant que le thème est posé (voir plus
  haut). Elles peuvent contenir des correctifs de sécurité.
- Un thème pose du CSS ; il ne lit ni n'envoie tes conversations. Tout est local.
- Claude ré-minifie son code à chaque version : si une mise à jour change la
  structure, le fond peut rester (le CSS cible surtout des tokens stables), mais
  certains éléments peuvent redevenir opaques — le Diagnostic les liste, à
  corriger dans `custom.css`, en attendant une mise à jour de l'outil.

## Développement

```bash
go test ./...        # patch/vérification d'intégrité/idempotence/restauration de app.asar, thème, galerie
go vet ./...
./claude-backdrop selftest --asar /chemin/vers/app.asar   # essaie le patch sur une copie, sans rien modifier
```

- `cmd/claude-backdrop` — la commande et l'aiguillage vers l'interface
- `internal/tui` — l'interface ([Bubble Tea](https://github.com/charmbracelet/bubbletea))
- `internal/backdrop` — réglages, galerie, installation et restauration
- `internal/asar` — lecture/patch de l'archive asar (décalage des offsets, empreintes) ;
  `testdata/electron.asar` a été écrit par `@electron/asar`
- `internal/macos` — Info.plist, signature, sauvegarde, fermeture/relance, `sips`
- `src/loader.js` + `src/page.js` — ce qui tourne dans Claude ; `src/theme.css` —
  le thème Ayu Dark ; tous embarqués dans le binaire par `src/embed.go`

L'ancienne version en Node (`src/*.mjs`, `test/`, `package.json`) reste en
secours : le lanceur s'en sert si Go n'est pas installé.

## Crédits

- Méthode de patch macOS (loader dans app.asar, re-signature, Info.plist)
  inspirée de projets communautaires :
  [PimpMyClaude](https://github.com/ElvisIglikov/PimpMyClaude),
  [claude-desktop-rtl-patch-mac](https://github.com/toboly/claude-desktop-rtl-patch-mac).
- Carte des tokens de thème de Claude Desktop :
  [claude-desktop-extra](https://github.com/patrickjaja/claude-desktop-extra).
- Palette [Ayu](https://github.com/ayu-theme).
- Interface : [Bubble Tea](https://github.com/charmbracelet/bubbletea),
  [Bubbles](https://github.com/charmbracelet/bubbles) et
  [Lip Gloss](https://github.com/charmbracelet/lipgloss) de Charm.
- Image par défaut : *La Mort de Socrate*, Jacques-Louis David, 1787, The Met,
  domaine public.

## Licence

MIT
