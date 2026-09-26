// Package asar is a minimal, dependency-free reader/patcher for Electron's asar
// archives.
//
// Layout of an asar file:
//
//	[0..4)   uint32 LE = 4               (size of the next field, Chromium Pickle)
//	[4..8)   uint32 LE = headerSize      (size of the header Pickle below)
//	[8..12)  uint32 LE = headerSize - 4  (payload size of the header Pickle)
//	[12..16) uint32 LE = jsonLength
//	[16..)   header JSON, padded to a multiple of 4
//	[8 + headerSize ..) file data; each entry's `offset` is relative to this point
//
// Electron's ASAR integrity check (macOS) compares SHA-256(header JSON) with
// ElectronAsarIntegrity in Info.plist, and each file entry carries its own
// SHA-256 block hashes. Patching a file therefore means: rewrite its bytes,
// shift the offsets of everything stored after it, refresh its integrity
// record, and hand the new header hash back to the caller.
package asar

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const defaultBlockSize = 4 * 1024 * 1024

// Archive is a parsed asar file. The header is re-parsed for every patch, so an
// Archive is never modified.
type Archive struct {
	data       []byte
	header     *object
	headerJSON []byte
	dataOffset int64
}

// Result of Patch / Unpatch.
type Result struct {
	Archive    []byte
	Changed    bool
	MainPath   string
	HeaderHash string
}

// Info describes an archive: its entry point, the loader it carries (if any)
// and the header hash Info.plist must pin.
type Info struct {
	MainPath   string
	LoaderTag  string
	HeaderHash string
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Parse reads the header of an asar archive.
func Parse(data []byte) (*Archive, error) {
	if len(data) < 16 || binary.LittleEndian.Uint32(data[0:4]) != 4 {
		return nil, errors.New("not an asar archive")
	}
	headerSize := int64(binary.LittleEndian.Uint32(data[4:8]))
	jsonLength := int64(binary.LittleEndian.Uint32(data[12:16]))
	if 16+jsonLength > int64(len(data)) || 8+headerSize > int64(len(data)) {
		return nil, errors.New("truncated asar archive")
	}
	headerJSON := data[16 : 16+jsonLength]
	value, err := decodeJSON(headerJSON)
	if err != nil {
		return nil, fmt.Errorf("unreadable asar header: %w", err)
	}
	header, ok := value.(*object)
	if !ok {
		return nil, errors.New("asar header is not a JSON object")
	}
	return &Archive{data: data, header: header, headerJSON: headerJSON, dataOffset: 8 + headerSize}, nil
}

// HeaderHash is what Electron compares with ElectronAsarIntegrity.
func (a *Archive) HeaderHash() string { return sha256Hex(a.headerJSON) }

func buildPrefix(header *object) (prefix, headerJSON []byte) {
	var buf bytes.Buffer
	encodeJSON(&buf, header)
	headerJSON = buf.Bytes()
	padding := (4 - len(headerJSON)%4) % 4
	headerSize := 8 + len(headerJSON) + padding
	prefix = make([]byte, 8+headerSize)
	binary.LittleEndian.PutUint32(prefix[0:4], 4)
	binary.LittleEndian.PutUint32(prefix[4:8], uint32(headerSize))
	binary.LittleEndian.PutUint32(prefix[8:12], uint32(headerSize-4))
	binary.LittleEndian.PutUint32(prefix[12:16], uint32(len(headerJSON)))
	copy(prefix[16:], headerJSON)
	return prefix, headerJSON
}

func findEntry(header *object, filePath string) (*object, error) {
	node := header
	for _, part := range strings.Split(filePath, "/") {
		if part == "" {
			continue
		}
		files := node.child("files")
		if files == nil || files.child(part) == nil {
			return nil, fmt.Errorf("%s not found in app.asar", filePath)
		}
		node = files.child(part)
	}
	if node.truthy("files") || node.truthy("link") {
		return nil, fmt.Errorf("%s is not a regular file", filePath)
	}
	if node.truthy("unpacked") {
		return nil, fmt.Errorf("%s is stored outside the archive (unpacked)", filePath)
	}
	return node, nil
}

// walkPacked visits every file stored inside the archive (not links, not
// unpacked files), in header order.
func walkPacked(node *object, visit func(entry *object, path string) error, prefix string) error {
	files := node.child("files")
	if files == nil {
		return nil
	}
	for _, name := range files.keys {
		child := files.child(name)
		if child == nil {
			continue
		}
		childPath := name
		if prefix != "" {
			childPath = prefix + "/" + name
		}
		if child.truthy("files") {
			if err := walkPacked(child, visit, childPath); err != nil {
				return err
			}
		} else if !child.truthy("link") && !child.truthy("unpacked") {
			if err := visit(child, childPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *Archive) slice(entry *object) ([]byte, error) {
	offset, err := entry.integer("offset")
	if err != nil {
		return nil, err
	}
	size, err := entry.integer("size")
	if err != nil {
		return nil, err
	}
	start := a.dataOffset + offset
	if offset < 0 || size < 0 || start+size > int64(len(a.data)) {
		return nil, errors.New("file data out of the archive bounds")
	}
	return a.data[start : start+size], nil
}

// ReadFile returns the bytes of a packed file.
func (a *Archive) ReadFile(filePath string) ([]byte, error) {
	entry, err := findEntry(a.header, filePath)
	if err != nil {
		return nil, err
	}
	return a.slice(entry)
}

// MainPath is the app's entry point: package.json "main"
// (".vite/build/index.pre.js" in current Claude builds). Never hardcode it: it
// has moved between releases.
func (a *Archive) MainPath() (string, error) {
	raw, err := a.ReadFile("package.json")
	if err != nil {
		return "", err
	}
	var pkg struct {
		Main any `json:"main"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return "", fmt.Errorf("package.json in app.asar: %w", err)
	}
	main, ok := pkg.Main.(string)
	if !ok || main == "" {
		return "", errors.New(`package.json in app.asar has no "main"`)
	}
	main = strings.TrimPrefix(main, "./")
	if _, err := findEntry(a.header, main); err != nil {
		main = strings.TrimSuffix(main, "/") + "/index.js"
	}
	return main, nil
}

func fileIntegrity(content []byte, blockSize int64) *object {
	if blockSize <= 0 {
		blockSize = defaultBlockSize
	}
	blocks := []any{}
	for offset := int64(0); offset < int64(len(content)); offset += blockSize {
		end := min(offset+blockSize, int64(len(content)))
		blocks = append(blocks, sha256Hex(content[offset:end]))
	}
	integrity := newObject()
	integrity.set("algorithm", "SHA256")
	integrity.set("hash", sha256Hex(content))
	integrity.set("blockSize", json.Number(strconv.FormatInt(blockSize, 10)))
	integrity.set("blocks", blocks)
	return integrity
}

// ---------------------------------------------------------------- loader block
// The loader is appended at the END of the main script, wrapped in markers, so
// the original code (and its "use strict" prologue) stays byte-for-byte intact
// and can be restored exactly.

var blockRE = regexp.MustCompile(`(?s)\n/\* claude-backdrop:loader:([\w.-]+):start \*/\n.*?\n/\* claude-backdrop:loader:[\w.-]+:end \*/\n$`)

var requireRE = regexp.MustCompile(`\brequire\(`)

// LoaderBlock wraps the loader code in its start/end markers.
func LoaderBlock(tag, code string) string {
	return fmt.Sprintf("\n/* claude-backdrop:loader:%s:start */\n%s\n/* claude-backdrop:loader:%s:end */\n", tag, strings.TrimSpace(code), tag)
}

// LoaderTagOf returns the tag of the loader appended to source, or "".
func LoaderTagOf(source string) string {
	if m := blockRE.FindStringSubmatch(source); m != nil {
		return m[1]
	}
	return ""
}

// StripLoader removes the loader block from source.
func StripLoader(source string) string {
	if loc := blockRE.FindStringIndex(source); loc != nil {
		return source[:loc[0]]
	}
	return source
}

// ---------------------------------------------------------------- patching

// replaceEntry swaps one packed file for new bytes and returns the new archive.
func (a *Archive) replaceEntry(filePath string, replacement []byte) (archive []byte, headerHash string, err error) {
	value, err := decodeJSON(a.headerJSON) // a fresh copy to modify
	if err != nil {
		return nil, "", err
	}
	header := value.(*object)
	entry, err := findEntry(header, filePath)
	if err != nil {
		return nil, "", err
	}
	offset, err := entry.integer("offset")
	if err != nil {
		return nil, "", err
	}
	size, err := entry.integer("size")
	if err != nil {
		return nil, "", err
	}
	data := a.data[a.dataOffset:]
	if offset < 0 || size < 0 || offset+size > int64(len(data)) {
		return nil, "", fmt.Errorf("%s: data out of the archive bounds", filePath)
	}

	// asar can deduplicate identical files: two entries may share bytes. If any
	// other entry points inside the region we rewrite, patching would corrupt it.
	err = walkPacked(header, func(other *object, _ string) error {
		if other == entry {
			return nil
		}
		start, err := other.integer("offset")
		if err != nil {
			return err
		}
		if start >= offset && start < offset+size {
			return fmt.Errorf("%s shares its bytes with another file", filePath)
		}
		return nil
	}, "")
	if err != nil {
		return nil, "", err
	}

	delta := int64(len(replacement)) - size
	err = walkPacked(header, func(other *object, _ string) error {
		start, err := other.integer("offset")
		if err != nil {
			return err
		}
		if other != entry && start > offset {
			other.set("offset", strconv.FormatInt(start+delta, 10))
		}
		return nil
	}, "")
	if err != nil {
		return nil, "", err
	}
	entry.set("size", json.Number(strconv.Itoa(len(replacement))))
	if entry.truthy("integrity") {
		var blockSize int64
		if integrity := entry.child("integrity"); integrity != nil {
			blockSize, _ = integrity.integer("blockSize")
		}
		entry.set("integrity", fileIntegrity(replacement, blockSize))
	}

	prefix, headerJSON := buildPrefix(header)
	out := make([]byte, 0, len(prefix)+len(data)+int(delta))
	out = append(out, prefix...)
	out = append(out, data[:offset]...)
	out = append(out, replacement...)
	out = append(out, data[offset+size:]...)
	return out, sha256Hex(headerJSON), nil
}

func (a *Archive) mainSource() (mainPath, source string, err error) {
	mainPath, err = a.MainPath()
	if err != nil {
		return "", "", err
	}
	raw, err := a.ReadFile(mainPath)
	if err != nil {
		return "", "", err
	}
	return mainPath, string(raw), nil
}

// Patch appends (or replaces) the loader in the main script. `tag` identifies
// the loader build; when the archive already carries the same tag nothing
// changes.
func Patch(data []byte, tag, code string) (Result, error) {
	a, err := Parse(data)
	if err != nil {
		return Result{}, err
	}
	mainPath, source, err := a.mainSource()
	if err != nil {
		return Result{}, err
	}
	if LoaderTagOf(source) == tag {
		return Result{Archive: data, MainPath: mainPath, HeaderHash: a.HeaderHash()}, nil
	}
	original := StripLoader(source)
	if !requireRE.MatchString(original) {
		return Result{}, fmt.Errorf("%s does not look like a CommonJS Electron entry point; refusing to patch blindly", mainPath)
	}
	archive, hash, err := a.replaceEntry(mainPath, []byte(original+LoaderBlock(tag, code)))
	if err != nil {
		return Result{}, err
	}
	return Result{Archive: archive, Changed: true, MainPath: mainPath, HeaderHash: hash}, nil
}

// Unpatch takes the loader out of the main script.
func Unpatch(data []byte) (Result, error) {
	a, err := Parse(data)
	if err != nil {
		return Result{}, err
	}
	mainPath, source, err := a.mainSource()
	if err != nil {
		return Result{}, err
	}
	if LoaderTagOf(source) == "" {
		return Result{Archive: data, MainPath: mainPath, HeaderHash: a.HeaderHash()}, nil
	}
	archive, hash, err := a.replaceEntry(mainPath, []byte(StripLoader(source)))
	if err != nil {
		return Result{}, err
	}
	return Result{Archive: archive, Changed: true, MainPath: mainPath, HeaderHash: hash}, nil
}

// Inspect reports the entry point, the loader tag and the header hash.
func Inspect(data []byte) (Info, error) {
	a, err := Parse(data)
	if err != nil {
		return Info{}, err
	}
	mainPath, source, err := a.mainSource()
	if err != nil {
		return Info{}, err
	}
	return Info{MainPath: mainPath, LoaderTag: LoaderTagOf(source), HeaderHash: a.HeaderHash()}, nil
}

// Verify checks every packed file against its integrity record (what Electron
// does lazily at runtime) and returns the number of files checked.
func Verify(data []byte) (int, error) {
	a, err := Parse(data)
	if err != nil {
		return 0, err
	}
	checked := 0
	err = walkPacked(a.header, func(entry *object, filePath string) error {
		integrity := entry.child("integrity")
		if integrity == nil {
			return nil
		}
		content, err := a.slice(entry)
		if err != nil {
			return fmt.Errorf("%s: %w", filePath, err)
		}
		blockSize, _ := integrity.integer("blockSize")
		expected := fileIntegrity(content, blockSize)
		hash, _ := integrity.get("hash")
		blocks, _ := integrity.get("blocks")
		if hash != expected.vals["hash"] || !sameBlocks(blocks, expected.vals["blocks"]) {
			return fmt.Errorf("integrity mismatch for %s", filePath)
		}
		checked++
		return nil
	}, "")
	return checked, err
}

func sameBlocks(a, b any) bool {
	x, ok1 := a.([]any)
	y, ok2 := b.([]any)
	if !ok1 || !ok2 || len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
