package internal

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPushArchiveToFilePath(t *testing.T) {
	reader := buildZipReaderForTest(t, map[string]string{"payload.txt": "hello"})
	target := filepath.Join(t.TempDir(), "renamed.txt")

	_, err := extractPushArchive(reader, target)
	if err != nil {
		t.Fatalf("extractPushArchive failed: %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("expected hello, got %q", string(content))
	}
}

func TestExtractPushArchiveToDirectoryPath(t *testing.T) {
	reader := buildZipReaderForTest(t, map[string]string{"payload.txt": "hello"})
	targetDir := filepath.Join(t.TempDir(), "dest") + string(os.PathSeparator)

	_, err := extractPushArchive(reader, targetDir)
	if err != nil {
		t.Fatalf("extractPushArchive failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(targetDir, "payload.txt"))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("expected hello, got %q", string(content))
	}
}

func buildZipReaderForTest(t *testing.T, files map[string]string) *zip.Reader {
	t.Helper()

	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	return reader
}
