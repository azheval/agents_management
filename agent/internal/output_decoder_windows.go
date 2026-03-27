//go:build windows

package internal

import (
	"golang.org/x/sys/windows"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
)

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
	procGetOEMCP           = kernel32.NewProc("GetOEMCP")
	procGetACP             = kernel32.NewProc("GetACP")
)

func preferredOutputDecoders() []encoding.Encoding {
	codePages := []uint32{
		callUint32Proc(procGetConsoleOutputCP),
		callUint32Proc(procGetOEMCP),
		callUint32Proc(procGetACP),
	}

	seen := map[uint32]bool{}
	decoders := make([]encoding.Encoding, 0, len(codePages))
	for _, cp := range codePages {
		if cp == 0 || seen[cp] {
			continue
		}
		seen[cp] = true
		if decoder := decoderForCodePage(cp); decoder != nil {
			decoders = append(decoders, decoder)
		}
	}
	return decoders
}

func callUint32Proc(proc *windows.LazyProc) uint32 {
	r, _, _ := proc.Call()
	return uint32(r)
}

func decoderForCodePage(codePage uint32) encoding.Encoding {
	switch codePage {
	case 866:
		return charmap.CodePage866
	case 1251:
		return charmap.Windows1251
	case 1252:
		return charmap.Windows1252
	default:
		return nil
	}
}
