package totp

import (
	"fmt"

	"github.com/skip2/go-qrcode"
)

// QRPixelSize is the rendered PNG dimension (BR-1.4). 256×256 strikes a
// balance between phone-scan reliability and HTTP payload size; bumping
// higher only matters for poor camera optics.
const QRPixelSize = 256

// RenderQRPNG produces a PNG-encoded QR code containing the supplied URI.
// Uses error-correction level Quartile (Q ≈ 25% recoverable damage) per
// BR-1.4 — robust against rendering imperfections without bloating the
// payload like H (≈ 30%) would.
func RenderQRPNG(uri string) ([]byte, error) {
	png, err := qrcode.Encode(uri, qrcode.Medium, QRPixelSize)
	if err != nil {
		return nil, fmt.Errorf("totp: render qr: %w", err)
	}
	return png, nil
}
