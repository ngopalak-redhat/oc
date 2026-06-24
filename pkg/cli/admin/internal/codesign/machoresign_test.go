package codesign

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTarGz writes a gzip-compressed tar archive at path containing the given
// regular-file entries keyed by entry name.
func writeTarGz(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating archive: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	for name, data := range entries {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0644,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing header %q: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("writing data %q: %v", name, err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
}

func TestExtractTarToTmpAndSign_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "malicious.tar.gz")
	sentinel := filepath.Join(dir, "escaped.txt")

	writeTarGz(t, archivePath, map[string][]byte{
		"../escaped.txt": []byte("owned"),
	})

	tempDir, err := extractTarToTmpAndSign(archivePath)
	if tempDir != "" {
		defer os.RemoveAll(tempDir)
	}
	if err == nil {
		t.Fatal("expected an error for path-traversal entry, got nil")
	}
	if tempDir != "" {
		t.Fatalf("expected empty tempDir on rejection, got %q", tempDir)
	}

	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatalf("path traversal succeeded: file written outside extraction dir at %s", sentinel)
	}
}

func TestExtractTarToTmpAndSign_ExtractsRegularFile(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "benign.tar.gz")

	writeTarGz(t, archivePath, map[string][]byte{
		"tool": []byte("not a macho binary"),
	})

	tempDir, err := extractTarToTmpAndSign(archivePath)
	if tempDir != "" {
		defer os.RemoveAll(tempDir)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tempDir == "" {
		t.Fatal("expected a non-empty tempDir on success")
	}
	if !strings.HasPrefix(filepath.Base(tempDir), "oc-release-extract-") {
		t.Fatalf("unexpected tempDir name: %q", tempDir)
	}

	extracted := filepath.Join(tempDir, "tool")
	if _, statErr := os.Stat(extracted); statErr != nil {
		t.Fatalf("expected extracted file at %s: %v", extracted, statErr)
	}
}
