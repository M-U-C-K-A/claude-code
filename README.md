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
s'applique avec `webContents.insertCSS(..., { cssOrigin: "user" })`, ce qui lui
donne la priorité sur les styles de l'app, plus un script de page qui rend
transparents les calques opaques que le CSS ne connaît pas par leur nom.

## Prérequis

- macOS avec [Claude Desktop](https://claude.ai/download) dans `/Applications`
- [Node.js](https://nodejs.org) 18 ou plus (`brew install node`)
- La première fois, macOS peut demander d'autoriser ton terminal dans
  **Réglages Système › Confidentialité et sécurité › Gestion des apps**.

## Installation

```bash
git clone https://github.com/M-U-C-K-A/claude-code.git
cd claude-code
./claude-backdrop install
```

L'installation télécharge l'image par défaut (*La Mort de Socrate*, domaine
public), sauvegarde Claude.app, pose le loader, re-signe et relance Claude.
Au premier lancement, macOS peut redemander le trousseau (« Claude Safe
Storage » → *Toujours autoriser*) et les autorisations micro/écran : c'est la
nouvelle signature locale, une seule fois.

Pour partir d'une autre image tout de suite :

```bash
./claude-backdrop install --image ~/Images/mon-tableau.jpg
```

## Au quotidien

```bash
./claude-backdrop gallery                     # les tableaux + (ré)active l'image au hasard par conversation
./claude-backdrop image ~/Images/autre.jpg    # fixe une image (fichier, URL, ou un id de la galerie) → coupe la rotation
./claude-backdrop set dim 0.65                 # assombrit plus l'image (0 à 0.95)
./claude-backdrop set imageblur 12             # floute l'image de fond (0 à 60 px)
./claude-backdrop set glass 0.4                # panneaux plus transparents (0 à 1)
./claude-backdrop set blur 24                  # flou du verre en px (0 = sans flou)
./claude-backdrop set rotate off               # image fixe au lieu d'une par conversation
./claude-backdrop set                          # liste tous les réglages
./claude-backdrop off                          # coupe le thème (loader gardé)
./claude-backdrop on                           # le remet
./claude-backdrop status                       # état du patch, signature, thème
./claude-backdrop doctor                       # relance l'analyse et liste les calques opaques restants
```

### Galerie et image par conversation

Par défaut, **chaque conversation reçoit un tableau au hasard**, stable pour
cette conversation (un rechargement garde le même). Les tableaux sont choisis
selon le mode : en **sombre**, *La Mort de Socrate*, *Le Serment des Horaces* ou
*Pandemonium* ; en **clair**, *L'École d'Athènes*. Tous sont du domaine public.

```bash
./claude-backdrop gallery         # liste et télécharge les tableaux, active la rotation
./claude-backdrop image socrates  # au contraire : une seule image fixe partout
./claude-backdrop set rotate on   # revenir à une image par conversation
```

Ids de la galerie : `socrates`, `horatii`, `pandemonium`, `school-of-athens`.

### Le bouton galerie dans Claude

Un petit bouton 🖼 apparaît **en haut à droite** de Claude. Il ouvre un panneau
où tu peux, sans passer par le terminal :

- voir les fonds disponibles et **changer celui de la fenêtre** d'un clic ;
- **ajouter ta propre image** (bouton ＋, elle rejoint la galerie) ;
- activer/couper l'**image au hasard par conversation** ;
- **Définir par défaut** l'image affichée (pour toutes les fenêtres).

Il gêne les icônes de la barre de titre ? Déplace-le dans `custom.css` :
`#cb-gallery-btn { right: 200px !important; }`.

Tout se recharge en direct. Tes propres règles CSS vont dans
`~/Library/Application Support/ClaudeBackdrop/custom.css` (jamais écrasé) ;
`theme.css` du même dossier est, lui, remplacé à chaque `install`.

## Mises à jour

Tant que le thème est posé, la signature d'Anthropic est remplacée par une
signature locale, et l'updater de Claude **refuse d'installer** les mises à jour
(il vérifie la signature). Le plus simple pour mettre Claude à jour :

```bash
./claude-backdrop restore   # remet le vrai Claude signé Anthropic → l'auto-update remarche
# … laisse Claude se mettre à jour (menu Claude › Rechercher les mises à jour), puis :
./claude-backdrop install   # repose le thème sur la nouvelle version
```

`restore` remet exactement le Claude.app d'origine à partir de la copie faite à
l'installation (signature comprise), donc rien n'est abîmé. Après une mise à
jour subie, `install` détecte la nouvelle version et repose tout proprement.

## Désinstaller

```bash
./claude-backdrop restore            # Claude revient à l'original
./claude-backdrop restore --purge    # + supprime réglages et image
```

Si la copie complète manque (installé avec `--no-app-backup`), `restore` retire
quand même le loader ; pour retrouver la signature d'Anthropic, réinstalle Claude
depuis [claude.ai/download](https://claude.ai/download).

## Réglages

| Réglage     | Défaut          | Rôle |
|-------------|-----------------|------|
| `rotate`    | `conversation`  | `on` = une image au hasard par conversation ; `off` = image fixe |
| `dim`       | `0.55`          | assombrissement de l'image (0 à 0.95) |
| `imageblur` | `6`             | flou de l'image de fond en px (0 à 60) |
| `glass`     | `0.5`           | opacité du verre (barre latérale, panneaux, terminal), 0 à 1 |
| `blur`      | `22`            | flou du verre en px (0 = sans flou) |
| `terminalopacity` | `0.82`    | opacité du terminal, 0.3 à 1 (1 = terminal opaque, sans transparence) |
| `position`  | `center`        | cadrage : `center`, `top`, `50% 20%`… |
| `size`      | `cover`         | `cover` (remplit) ou `contain` |
| `mode`      | `dark`          | `dark`, `light` ou `auto` (suit le thème de Claude) |
| `autoClear` | `true`          | détection automatique des calques opaques (barre latérale, terminal…) |

## Sécurité et limites

- La signature d'Anthropic (et son *hardened runtime*) est remplacée par une
  signature locale : l'app est moins protégée contre l'injection de code.
- Les mises à jour automatiques échouent tant que le thème est posé (voir plus
  haut). Elles peuvent contenir des correctifs de sécurité.
- Un thème pose du CSS ; il ne lit ni n'envoie tes conversations. Tout est local.
- Claude ré-minifie son code à chaque version : si une mise à jour change la
  structure, le fond peut rester (le CSS cible surtout des tokens stables), mais
  certains éléments peuvent redevenir opaques — `claude-backdrop doctor` les
  liste, à corriger dans `custom.css`, en attendant une mise à jour de l'outil.

## Développement

```bash
npm install      # dépendance de test uniquement (@electron/asar)
npm test         # patch/vérification d'intégrité/idempotence/restauration de app.asar
./claude-backdrop selftest --asar /chemin/vers/app.asar   # essaie le patch sur une copie, sans rien modifier
```

- `src/asar.mjs` — lecture/patch de l'archive asar (décalage des offsets, empreintes)
- `src/loader.js` + `src/page.js` — ce qui tourne dans Claude (assemblés par `src/build.mjs`)
- `src/theme.css` — le thème Ayu Dark
- `src/macos.mjs` — Info.plist, signature, sauvegarde, fermeture/relance
- `src/cli.mjs` — la commande `claude-backdrop`

## Crédits

- Méthode de patch macOS (loader dans app.asar, re-signature, Info.plist)
  inspirée de projets communautaires :
  [PimpMyClaude](https://github.com/ElvisIglikov/PimpMyClaude),
  [claude-desktop-rtl-patch-mac](https://github.com/toboly/claude-desktop-rtl-patch-mac).
- Carte des tokens de thème de Claude Desktop :
  [claude-desktop-extra](https://github.com/patrickjaja/claude-desktop-extra).
- Palette [Ayu](https://github.com/ayu-theme).
- Image par défaut : *La Mort de Socrate*, Jacques-Louis David, 1787, The Met,
  domaine public.

## Licence

MIT
