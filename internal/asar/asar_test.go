package asar

// Tests for the risky part: patching app.asar without corrupting it.
//
// testdata/electron.asar was written by @electron/asar (the library Electron's
// own tooling uses), laid out like Claude's: package.json -> .vite/build/
// index.pre.js, files packed before and after the main script, a unicode path,
// and two identical files. The tests check that:
//   - the header round-trips byte for byte (same bytes as JSON.stringify),
//   - the loader lands in the main script and only there,
//   - every other file still reads back at its offset, integrity included,
//   - the header hash handed back matches the rebuilt archive,
//   - patching is idempotent and fully reversible, byte for byte.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
)

const testTag = "9.9.9-abcdef0123"
const testCode = "// loader\n;(function(){ require('electron'); })();\n"

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/electron.asar")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// snapshot maps every packed file to the SHA-256 of its bytes.
func snapshot(t *testing.T, data []byte) map[string][32]byte {
	t.Helper()
	a, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][32]byte{}
	err = walkPacked(a.header, func(entry *object, path string) error {
		content, err := a.slice(entry)
		files[path] = sha256.Sum256(content)
		return err
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

type packed struct{ path, content string }

// pack writes an asar archive the way @electron/asar does: files in the given
// order, offsets as strings, sizes as numbers, per-file integrity.
func pack(t *testing.T, files []packed) []byte {
	t.Helper()
	header := newObject()
	var data bytes.Buffer
	for _, f := range files {
		node := header
		parts := strings.Split(f.path, "/")
		for _, dir := range parts[:len(parts)-1] {
			children := node.child("files")
			if children == nil {
				children = newObject()
				node.set("files", children)
			}
			if children.child(dir) == nil {
				children.set(dir, newObject())
			}
			node = children.child(dir)
		}
		children := node.child("files")
		if children == nil {
			children = newObject()
			node.set("files", children)
		}
		entry := newObject()
		entry.set("size", json.Number(strconv.Itoa(len(f.content))))
		entry.set("offset", strconv.Itoa(data.Len()))
		entry.set("integrity", fileIntegrity([]byte(f.content), defaultBlockSize))
		children.set(parts[len(parts)-1], entry)
		data.WriteString(f.content)
	}
	prefix, _ := buildPrefix(header)
	return append(prefix, data.Bytes()...)
}

func claudeLike(main string) []packed {
	return []packed{
		{"package.json", `{"name":"claude","main":"./` + main + `"}`},
		{"a-first.js", "module.exports = 'before';\n" + strings.Repeat("a", 3000)},
		{main, "\"use strict\";\nconst electron = require(\"electron\");\nconsole.log(electron);\n"},
		{"z/after-1.js", strings.Repeat("z", 5000)},
		{"z/after-2.css", strings.Repeat("body{color:red}", 400)},
	}
}

func TestFixtureIsReadable(t *testing.T) {
	data := fixture(t)
	info, err := Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.MainPath != ".vite/build/index.pre.js" || info.LoaderTag != "" {
		t.Fatalf("unexpected info: %+v", info)
	}
	checked, err := Verify(data)
	if err != nil {
		t.Fatal(err)
	}
	if checked < 9 {
		t.Fatalf("expected the fixture's files to be checked, got %d", checked)
	}
}

func TestHeaderRoundTripsByteForByte(t *testing.T) {
	a, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	encodeJSON(&buf, a.header)
	if !bytes.Equal(buf.Bytes(), a.headerJSON) {
		t.Fatalf("header re-encoded differently:\n got %s\nwant %s", buf.Bytes(), a.headerJSON)
	}
}

func TestPatchChangesOnlyTheMainScript(t *testing.T) {
	for name, data := range map[string][]byte{"fixture": fixture(t), "packed": pack(t, claudeLike("app/main.js"))} {
		t.Run(name, func(t *testing.T) {
			before := snapshot(t, data)
			result, err := Patch(data, testTag, testCode)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Changed {
				t.Fatal("patch reported no change")
			}
			after := snapshot(t, result.Archive)
			for file, hash := range before {
				if file == result.MainPath {
					if after[file] == hash {
						t.Errorf("main script %s did not change", file)
					}
				} else if after[file] != hash {
					t.Errorf("%s must be byte-identical after patch", file)
				}
			}
			a, _ := Parse(result.Archive)
			main, _ := a.ReadFile(result.MainPath)
			if !strings.HasPrefix(string(main), `"use strict"`) {
				t.Error("original prologue not preserved")
			}
			if LoaderTagOf(string(main)) != testTag {
				t.Error("loader block missing from the main script")
			}
		})
	}
}

func TestPatchedArchivePassesIntegrity(t *testing.T) {
	result, err := Patch(fixture(t), testTag, testCode)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := Verify(result.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if checked < 9 {
		t.Fatalf("expected many files checked, got %d", checked)
	}
}

func TestHeaderHashMatchesRebuiltArchive(t *testing.T) {
	result, err := Patch(fixture(t), testTag, testCode)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Inspect(result.Archive)
	if err != nil {
		t.Fatal(err)
	}
	if info.HeaderHash != result.HeaderHash {
		t.Fatalf("hash %s, archive says %s", result.HeaderHash, info.HeaderHash)
	}
	// The pickle sizes must agree with the new header.
	headerSize := binary.LittleEndian.Uint32(result.Archive[4:8])
	if binary.LittleEndian.Uint32(result.Archive[8:12]) != headerSize-4 || headerSize%4 != 0 {
		t.Fatal("inconsistent pickle sizes")
	}
}

func TestPatchIsIdempotent(t *testing.T) {
	once, err := Patch(fixture(t), testTag, testCode)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := Patch(once.Archive, testTag, testCode)
	if err != nil {
		t.Fatal(err)
	}
	if twice.Changed || !bytes.Equal(once.Archive, twice.Archive) {
		t.Fatal("second patch with the same tag changed the archive")
	}
	// A new loader replaces the old one instead of stacking.
	again, err := Patch(once.Archive, "9.9.9-0000000000", testCode)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Parse(again.Archive)
	main, _ := a.ReadFile(again.MainPath)
	if strings.Count(string(main), ":start */") != 1 {
		t.Fatal("loader blocks stacked up")
	}
}

func TestUnpatchRestoresTheArchiveExactly(t *testing.T) {
	for name, data := range map[string][]byte{"fixture": fixture(t), "packed": pack(t, claudeLike("app/main.js"))} {
		t.Run(name, func(t *testing.T) {
			patched, err := Patch(data, testTag, testCode)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := Unpatch(patched.Archive)
			if err != nil {
				t.Fatal(err)
			}
			if !restored.Changed {
				t.Fatal("unpatch reported no change")
			}
			if !bytes.Equal(restored.Archive, data) {
				t.Fatal("restored archive differs from the original")
			}
			if nothing, _ := Unpatch(data); nothing.Changed {
				t.Fatal("unpatching a clean archive changed it")
			}
		})
	}
}

func TestMainFallsBackToIndexJS(t *testing.T) {
	files := claudeLike("app/index.js")
	files[0] = packed{"package.json", `{"main":"./app"}`}
	info, err := Inspect(pack(t, files))
	if err != nil {
		t.Fatal(err)
	}
	if info.MainPath != "app/index.js" {
		t.Fatalf("main resolved to %q", info.MainPath)
	}
}

func TestRefusesNonCommonJSEntryPoint(t *testing.T) {
	data := pack(t, []packed{
		{"package.json", `{"main":"./main.js"}`},
		{"main.js", "export const x = 1; // ESM, no require\n"},
	})
	if _, err := Patch(data, testTag, testCode); err == nil || !strings.Contains(err.Error(), "does not look like") {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestRefusesSharedBytes(t *testing.T) {
	data := pack(t, claudeLike("app/main.js"))
	a, _ := Parse(data)
	// Point another entry inside the main script's bytes, like asar dedup does.
	main, _ := findEntry(a.header, "app/main.js")
	other, _ := findEntry(a.header, "z/after-1.js")
	other.set("offset", main.vals["offset"])
	prefix, _ := buildPrefix(a.header)
	tampered := append(prefix, data[a.dataOffset:]...)
	if _, err := Patch(tampered, testTag, testCode); err == nil || !strings.Contains(err.Error(), "shares its bytes") {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestLoaderBlockHelpers(t *testing.T) {
	source := "\"use strict\";\nrequire('x');\n"
	patched := source + LoaderBlock(testTag, testCode)
	if LoaderTagOf(patched) != testTag || LoaderTagOf(source) != "" {
		t.Fatal("tag detection failed")
	}
	if StripLoader(patched) != source {
		t.Fatal("strip did not restore the source")
	}
}

func TestRejectsGarbage(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("hello world, not an asar"), make([]byte, 64)} {
		if _, err := Parse(data); err == nil {
			t.Fatalf("parsed garbage %q", data)
		}
	}
}
