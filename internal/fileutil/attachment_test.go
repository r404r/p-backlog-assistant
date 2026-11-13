package fileutil

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeAttachmentFilename(t *testing.T) {
	for _, pair := range [][2]string{{"案内/図.png", "案内_図.png"}, {`案内\図.png`, "案内_図.png"}, {"../案内.txt", ".._案内.txt"}, {"..", "_"}, {"", "_"}, {"a\n\x00b.txt", "a__b.txt"}, {"CON", "CON_"}, {"COM1.txt", "COM1_.txt"}, {"PRN.tar.gz", "PRN_.tar.gz"}, {"COM¹.pdf", "COM¹_.pdf"}, {"計画.txt. ", "計画.txt"}, {".txt", "_.txt"}, {strings.Repeat("a", 300) + ".pdf", strings.Repeat("a", 251) + ".pdf"}, {strings.Repeat("あ", 100) + ".txt", strings.Repeat("あ", 83) + ".txt"}} {
		if got := SafeAttachmentFilename(pair[0]); got != pair[1] || len(got) > 255 || !utf8.ValidString(got) {
			t.Errorf("%q => %q, want %q", pair[0], got, pair[1])
		}
	}
}

func TestAtomicStreamPreservesTarget(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		dir := t.TempDir()
		target := filepath.Join(dir, "sample.txt")
		if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, stop := context.WithCancel(context.Background())
		err := WriteAtomic(ctx, target, ".wiki-attachment-*.tmp", func(w io.Writer) error {
			_, _ = io.WriteString(w, "partial")
			if cancel {
				stop()
				return nil
			}
			return errors.New("https://example.invalid?apiKey=FAKE-SECRET /outside-private/sample")
		}, nil)
		stop()
		data, _ := os.ReadFile(target)
		files, _ := os.ReadDir(dir)
		if err == nil || string(data) != "original" || len(files) != 1 {
			t.Fatalf("err=%v data=%q files=%d", err, data, len(files))
		}
	}
}
