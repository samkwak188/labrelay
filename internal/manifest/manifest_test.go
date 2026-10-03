package manifest

import (
	"bytes"
	"strings"
	"testing"
)

func fixture() Manifest {
	return Manifest{Schema: 1, Name: "test", Source: "generic", Files: []File{{Path: "a", Role: "data", Size: 0, SHA256: Digest(nil)}}}
}
func TestDeterministicAndExactBytes(t *testing.T) {
	m := fixture()
	m.Files = append(m.Files, File{Path: "z", Role: "data", Size: 1, SHA256: Digest([]byte("z"))})
	a, e := m.Bytes()
	if e != nil {
		t.Fatal(e)
	}
	m.Files[0], m.Files[1] = m.Files[1], m.Files[0]
	b, e := m.Bytes()
	if e != nil || !bytes.Equal(a, b) {
		t.Fatal("not deterministic", e)
	}
	if _, e = Parse(a); e != nil {
		t.Fatal(e)
	}
	if Digest(a) == Digest(bytes.TrimSpace(a)) {
		t.Fatal("identity must include exact bytes")
	}
}
func TestRejectAmbiguousAndUnsafe(t *testing.T) {
	for _, p := range []string{"/x", "../x", "a/../x", "a\\x", "a//b", ".", "a/", "C:x", "a\x00b"} {
		t.Run(p, func(t *testing.T) {
			if PathOK(p) {
				t.Fatal("accepted unsafe path")
			}
		})
	}
	for _, p := range []string{"a", "a/b"} {
		m := fixture()
		m.Files = append(m.Files, File{Path: p, Role: "data", SHA256: Digest(nil)})
		if _, e := m.Bytes(); e == nil {
			t.Fatal("accepted duplicate/collision")
		}
	}
	b, _ := fixture().Bytes()
	b = bytes.Replace(b, []byte(`"name":"test"`), []byte(`"name":"x","name":"test"`), 1)
	if _, e := Parse(b); e == nil {
		t.Fatal("accepted duplicate JSON key")
	}
	b, _ = fixture().Bytes()
	if _, e := Parse(append(b, []byte(`{}`)...)); e == nil {
		t.Fatal("accepted trailing JSON")
	}
	m := fixture()
	m.Files[0].SHA256 = strings.Repeat("A", 64)
	if _, e := m.Bytes(); e == nil {
		t.Fatal("accepted noncanonical hash")
	}
}
func FuzzManifest(f *testing.F) {
	b, _ := fixture().Bytes()
	f.Add(b)
	f.Add([]byte(`{"name":"a","name":"b"}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, e := Parse(b)
		if e == nil {
			if _, e = m.Bytes(); e != nil {
				t.Fatal(e)
			}
		}
	})
}
func FuzzPath(f *testing.F) {
	for _, p := range []string{"a/b", "../x", "a\\b", "/", "x"} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p string) {
		if PathOK(p) && (strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "\x00")) {
			t.Fatal(p)
		}
	})
}
