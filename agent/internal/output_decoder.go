package internal

import (
	"bytes"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

func decodeProcessOutputLine(raw []byte) string {
	line := bytes.TrimSuffix(raw, []byte{'\r'})
	if utf8.Valid(line) {
		return string(line)
	}

	candidates := make([]string, 0, 5)

	if decoded, ok := decodeUTF16Line(line); ok {
		candidates = append(candidates, decoded)
	}

	for _, decoder := range append(preferredOutputDecoders(), charmap.CodePage866, charmap.Windows1251, charmap.Windows1252) {
		if decoder == nil {
			continue
		}
		decoded, err := decoder.NewDecoder().Bytes(line)
		if err == nil && utf8.Valid(decoded) {
			candidates = append(candidates, string(decoded))
		}
	}

	if len(candidates) == 0 {
		return string(line)
	}

	best := candidates[0]
	bestScore := scoreDecodedText(best)
	for _, candidate := range candidates[1:] {
		score := scoreDecodedText(candidate)
		if score > bestScore {
			best = candidate
			bestScore = score
		}
	}

	return best
}

func decodeUTF16Line(raw []byte) (string, bool) {
	if len(raw) < 2 || len(raw)%2 != 0 {
		return "", false
	}

	zeroCount := 0
	for _, b := range raw {
		if b == 0 {
			zeroCount++
		}
	}
	if zeroCount == 0 {
		return "", false
	}

	u16 := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		u16 = append(u16, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(u16)), true
}

func scoreDecodedText(text string) int {
	score := 0
	for _, r := range text {
		switch {
		case r == utf8.RuneError:
			score -= 10
		case unicode.Is(unicode.Cyrillic, r):
			score += 6
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			score += 3
		case unicode.IsSpace(r):
			score += 2
		case unicode.IsPunct(r):
			score += 1
		case isBoxDrawingOrBlockRune(r):
			score -= 8
		case unicode.IsControl(r):
			score -= 6
		default:
			score -= 1
		}
	}
	return score
}

func isBoxDrawingOrBlockRune(r rune) bool {
	return (r >= 0x2500 && r <= 0x257F) || (r >= 0x2580 && r <= 0x259F)
}
