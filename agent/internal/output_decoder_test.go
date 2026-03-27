package internal

import (
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestDecodeProcessOutputLineUTF8(t *testing.T) {
	got := decodeProcessOutputLine([]byte("hello мир"))
	if got != "hello мир" {
		t.Fatalf("expected UTF-8 to stay intact, got %q", got)
	}
}

func TestDecodeProcessOutputLineCP866(t *testing.T) {
	raw, err := charmap.CodePage866.NewEncoder().Bytes([]byte("Обмен пакетами с 127.0.0.1"))
	if err != nil {
		t.Fatalf("failed to encode cp866: %v", err)
	}

	got := decodeProcessOutputLine(raw)
	if got != "Обмен пакетами с 127.0.0.1" {
		t.Fatalf("expected decoded cp866 text, got %q", got)
	}
}

func TestDecodeProcessOutputLineWindows1251(t *testing.T) {
	raw, err := charmap.Windows1251.NewEncoder().Bytes([]byte("Превышен интервал ожидания"))
	if err != nil {
		t.Fatalf("failed to encode windows-1251: %v", err)
	}

	got := decodeProcessOutputLine(raw)
	if got != "Превышен интервал ожидания" {
		t.Fatalf("expected decoded windows-1251 text, got %q", got)
	}
}
