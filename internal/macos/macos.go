// Package macos is the macOS plumbing: Info.plist integrity hashes, ad-hoc
// re-signing, bundle copies, quitting / relaunching Claude, and image
// conversion with sips. Everything shells out to the system tools.
package macos

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	BundleID     = "com.anthropic.claudefordesktop"
	plistBuddy   = "/usr/libexec/PlistBuddy"
	integrityKey = ":ElectronAsarIntegrity:Resources/app.asar:hash"
	codesignTool = "/usr/bin/codesign"
	sipsTool     = "/usr/bin/sips"
	pollInterval = 250 * time.Millisecond
	quitTimeout  = 15 * time.Second // after asking politely
	termTimeout  = 10 * time.Second // after SIGTERM
	killTimeout  = 5 * time.Second  // after SIGKILL
)

// run executes a tool and returns its stdout; a failure carries its stderr.
func run(exe string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(exe, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s : %s", filepath.Base(exe), msg)
	}
	return stdout.String(), nil
}

// attempt executes a tool and reports its output and exit status, never failing.
func attempt(exe string, args ...string) (stdout, stderr string, ok bool) {
	var out, errOut bytes.Buffer
	cmd := exec.Command(exe, args...)
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	return out.String(), errOut.String(), err == nil
}

// Paths inside Claude.app.
type Paths struct {
	App, Asar, Resources, Info string
}

func PathsOf(app string) Paths {
	return Paths{
		App:       app,
		Asar:      filepath.Join(app, "Contents", "Resources", "app.asar"),
		Resources: filepath.Join(app, "Contents", "Resources"),
		Info:      filepath.Join(app, "Contents", "Info.plist"),
	}
}

func AppVersion(app string) (string, error) {
	out, err := run(plistBuddy, "-c", "Print :CFBundleShortVersionString", PathsOf(app).Info)
	return strings.TrimSpace(out), err
}

// PlistHash is one Info.plist that pins the app.asar header hash.
type PlistHash struct {
	Plist, Hash string
}

// IntegrityPlists lists every Info.plist in the bundle that pins the app.asar
// header hash: the main app and, depending on the build, the Electron framework
// and the helpers.
func IntegrityPlists(app string) []PlistHash {
	candidates := []string{PathsOf(app).Info}
	frameworks := filepath.Join(app, "Contents", "Frameworks")
	if entries, err := os.ReadDir(frameworks); err == nil {
		for _, e := range entries {
			name := e.Name()
			if strings.HasSuffix(name, ".app") {
				candidates = append(candidates, filepath.Join(frameworks, name, "Contents", "Info.plist"))
			}
			if strings.HasSuffix(name, ".framework") {
				candidates = append(candidates, filepath.Join(frameworks, name, "Versions", "A", "Resources", "Info.plist"))
			}
		}
	}
	var found []PlistHash
	for _, plist := range candidates {
		out, _, ok := attempt(plistBuddy, "-c", "Print "+integrityKey, plist)
		if hash := strings.TrimSpace(out); ok && hash != "" {
			found = append(found, PlistHash{Plist: plist, Hash: hash})
		}
	}
	return found
}

func SetIntegrity(plist, hash string) error {
	_, err := run(plistBuddy, "-c", fmt.Sprintf("Set %s %s", integrityKey, hash), plist)
	return err
}

// Signature: Kind is "developer-id" for Anthropic's own signature, "adhoc"
// once re-signed locally, "unsigned" or "unknown".
type Signature struct {
	Kind  string
	Valid bool
	Team  string
}

var teamRE = regexp.MustCompile(`TeamIdentifier=(\S+)`)

// SignatureOf inspects the code signature. The strict deep verification takes
// a few seconds on Claude's bundle.
func SignatureOf(app string) Signature {
	stdout, stderr, _ := attempt(codesignTool, "-dv", "--verbose=2", app)
	text := stdout + "\n" + stderr
	_, _, valid := attempt(codesignTool, "--verify", "--deep", "--strict", app)
	sig := Signature{Kind: "unknown", Valid: valid}
	switch {
	case strings.Contains(text, "Signature=adhoc"):
		sig.Kind = "adhoc"
	case strings.Contains(text, "Authority=Developer ID Application"):
		sig.Kind = "developer-id"
	case strings.Contains(strings.ToLower(text), "not signed"):
		sig.Kind = "unsigned"
	}
	if m := teamRE.FindStringSubmatch(text); m != nil && m[1] != "not" {
		sig.Team = m[1]
	}
	return sig
}

// Resign re-signs the bundle locally (ad-hoc): patching app.asar invalidates
// Anthropic's signature. Nested code first (--deep), then the app itself with
// the entitlements Claude needs (microphone, camera, the Cowork VM...).
func Resign(app string, entitlements []byte) error {
	file, err := os.CreateTemp("", "claude-backdrop-entitlements-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(entitlements); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	attempt("/usr/bin/xattr", "-cr", app)
	steps := [][]string{
		{"--force", "--deep", "--sign", "-", "--timestamp=none", app},
		{"--force", "--sign", "-", "--timestamp=none", "--entitlements", file.Name(), app},
		{"--verify", "--deep", "--strict", app},
	}
	for _, args := range steps {
		if _, err := run(codesignTool, args...); err != nil {
			return err
		}
	}
	return nil
}

// CopyBundle makes a full copy of the bundle, signature included (ditto keeps
// everything).
func CopyBundle(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	_, err := run("/usr/bin/ditto", from, to)
	return err
}

// ---------------------------------------------------------------- processes

var psLineRE = regexp.MustCompile(`^\s*(\d+)\s+(.*)$`)

func claudeProcesses(app string) (main, helpers []int) {
	listing, _, _ := attempt("/bin/ps", "-axo", "pid=,command=")
	for _, line := range strings.Split(listing, "\n") {
		m := psLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pid, _ := strconv.Atoi(m[1])
		switch {
		case strings.HasPrefix(m[2], app+"/Contents/MacOS/"):
			main = append(main, pid)
		case strings.HasPrefix(m[2], app+"/Contents/"):
			helpers = append(helpers, pid)
		}
	}
	return main, helpers
}

func IsRunning(app string) bool {
	main, _ := claudeProcesses(app)
	return len(main) > 0
}

// RunningInside tells whether this process runs in a terminal that Claude
// itself hosts (Claude Code inside the desktop app): quitting Claude would
// kill it halfway through.
func RunningInside(app string) bool {
	pid := os.Getpid()
	for i := 0; i < 40 && pid > 1; i++ {
		out, _, _ := attempt("/bin/ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid))
		m := psLineRE.FindStringSubmatch(strings.TrimSpace(out))
		if m == nil {
			return false
		}
		if strings.HasPrefix(m[2], app+"/Contents/") {
			return true
		}
		pid, _ = strconv.Atoi(m[1])
	}
	return false
}

func signal(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(sig)
		}
	}
}

func waitGone(app string, timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(pollInterval) {
		if !IsRunning(app) {
			return true
		}
	}
	return !IsRunning(app)
}

// Quit closes Claude: politely first, then SIGTERM, then SIGKILL. Reports
// whether it was running.
func Quit(app string) (wasRunning bool, err error) {
	if !IsRunning(app) {
		return false, nil
	}
	attempt("/usr/bin/osascript", "-e", fmt.Sprintf("tell application id %q to quit", BundleID))
	if waitGone(app, quitTimeout) {
		return true, nil
	}
	main, _ := claudeProcesses(app)
	signal(main, syscall.SIGTERM)
	if waitGone(app, termTimeout) {
		return true, nil
	}
	main, helpers := claudeProcesses(app)
	signal(append(main, helpers...), syscall.SIGKILL)
	if waitGone(app, killTimeout) {
		return true, nil
	}
	return true, errors.New("Claude refuse de se fermer : quitte-le à la main (⌘Q) puis relance")
}

func Launch(app string) {
	attempt("/usr/bin/open", "-a", app)
}

// ---------------------------------------------------------------- images

func HasSips() bool {
	_, err := os.Stat(sipsTool)
	return err == nil
}

var (
	widthRE  = regexp.MustCompile(`pixelWidth:\s*(\d+)`)
	heightRE = regexp.MustCompile(`pixelHeight:\s*(\d+)`)
)

func ImageSize(file string) (w, h int, err error) {
	out, err := run(sipsTool, "-g", "pixelWidth", "-g", "pixelHeight", file)
	if err != nil {
		return 0, 0, err
	}
	if m := widthRE.FindStringSubmatch(out); m != nil {
		w, _ = strconv.Atoi(m[1])
	}
	if m := heightRE.FindStringSubmatch(out); m != nil {
		h, _ = strconv.Atoi(m[1])
	}
	if w == 0 || h == 0 {
		return 0, 0, fmt.Errorf("%s n'est pas une image lisible", filepath.Base(file))
	}
	return w, h, nil
}

// ToJpeg converts to JPEG, longest side at most max pixels (never upscaled).
func ToJpeg(input, output string, max int) (w, h int, err error) {
	w, h, err = ImageSize(input)
	if err != nil {
		return 0, 0, err
	}
	args := []string{"-s", "format", "jpeg", "-s", "formatOptions", "85"}
	if w > max || h > max {
		args = append(args, "-Z", strconv.Itoa(max))
	}
	if _, err := run(sipsTool, append(args, input, "--out", output)...); err != nil {
		return 0, 0, err
	}
	return ImageSize(output)
}
