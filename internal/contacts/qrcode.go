package contacts

import qrcode "github.com/skip2/go-qrcode"

// generateQRCode encodes the given text into a PNG QR code image at
// medium error correction level. Returns the PNG bytes.
func generateQRCode(text string) ([]byte, error) {
	return qrcode.Encode(text, qrcode.Medium, 512)
}
