package totp

import (
	"bytes"
	"image/png"
	"testing"
)

// Scenario: 2.4-UNIT-010 — RenderQRPNG produces a valid PNG image.
func TestRenderQRPNG_ValidPNG(t *testing.T) {
	t.Parallel()
	data, err := RenderQRPNG("otpauth://totp/He-API:user@example.com?secret=ABCDEFGHIJKLMNOPQRST&issuer=He-API&algorithm=SHA1&digits=6&period=30")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("empty png")
	}
	// PNG magic bytes.
	if !bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4E, 0x47}) {
		t.Fatalf("not a PNG: %x", data[:4])
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != QRPixelSize || b.Dy() != QRPixelSize {
		t.Fatalf("dim=%dx%d want %dx%d", b.Dx(), b.Dy(), QRPixelSize, QRPixelSize)
	}
}
