package httpapi

import "testing"

// The extension alone decides nothing: an author who renames a .docx to .png
// must be refused, and a page photographed on a phone must be accepted.
func TestMediaTypeAcceptsWhatAuthorsActuallyUpload(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), 0x00, 0x01)
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	mp3ID3 := []byte("ID3\x04\x00")
	mp3Sync := []byte{0xff, 0xfb, 0x90, 0x00}

	for _, c := range []struct {
		name     string
		filename string
		data     []byte
		want     string
	}{
		{"png scan", "page-7.png", png, "image/png"},
		{"jpeg scan", "page-7.jpg", jpeg, "image/jpeg"},
		{"jpeg long extension", "page-7.jpeg", jpeg, "image/jpeg"},
		{"jpeg uppercase extension", "PAGE-7.JPG", jpeg, "image/jpeg"},
		{"tagged mp3", "section-1.mp3", mp3ID3, "audio/mpeg"},
		{"untagged mp3", "section-1.mp3", mp3Sync, "audio/mpeg"},
		{"jpeg bytes named png", "page-7.png", jpeg, ""},
		{"png bytes named jpeg", "page-7.jpg", png, ""},
		{"document renamed to png", "report.png", []byte("PK\x03\x04zipped"), ""},
		{"no extension", "page-7", png, ""},
		{"truncated jpeg", "page-7.jpg", []byte{0xff, 0xd8}, ""},
		{"empty", "page-7.png", nil, ""},
	} {
		if got := mediaType(c.data, c.filename); got != c.want {
			t.Errorf("%s: mediaType(%q) = %q, want %q", c.name, c.filename, got, c.want)
		}
	}
}
