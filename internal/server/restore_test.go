package server

import (
	"context"
	"testing"
)

func TestVerifyVersionRejectsUnpinnedObjects(t *testing.T) {
	for _, tc := range []struct {
		name, key, version string
	}{
		{"missing-key", "", "specific-version"},
		{"missing-version", "uploads/object", ""},
		{"null-version", "uploads/object", "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil storage client ensures rejection happens before any storage request.
			s := &Server{}
			if err := s.VerifyVersion(context.Background(), tc.key, tc.version, "", 0); err == nil {
				t.Fatal("verification accepted an unpinned object")
			}
		})
	}
}
