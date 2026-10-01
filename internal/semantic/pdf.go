package semantic

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/ledongthuc/pdf"
)

const maxPDFBytes = 64 << 20

// pdfText extracts plain text from a PDF (pure Go).
func pdfText(path string) (text string, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}

	if st.Size() > maxPDFBytes {
		return "", fmt.Errorf("pdf larger than %d bytes", maxPDFBytes)
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf parse panic: %v", r)
		}
	}()

	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	rd, err := r.GetPlainText()
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(rd, 4<<20)); err != nil {
		return "", err
	}

	return buf.String(), nil
}
