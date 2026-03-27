//go:build !windows

package internal

import "golang.org/x/text/encoding"

func preferredOutputDecoders() []encoding.Encoding {
	return nil
}
