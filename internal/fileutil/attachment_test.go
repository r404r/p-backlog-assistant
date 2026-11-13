package fileutil

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSafeAttachmentFilename(t *testing.T) {
	for _, pair := range [][2]string{
		{".", "_"}, {"nul", "nul_"}, {"lpt9.csv", "lpt9_.csv"}, {"AUX.log", "AUX_.log"},
		{"name... ", "name"}, {".環境", "_.環境"}, {"a." + strings.Repeat("x", 40), "a." + strings.Repeat("x", 40)},
		{"全角ＡＢＣ１２３．資料.txt", "全角ＡＢＣ１２３．資料.txt"}, {"計画:案?内*.pdf", "計画_案_内_.pdf"},
		{"a\x7f\u0085b.txt", "a__b.txt"}, {"a\xffb.txt", "a_b.txt"},
	} {
		if got := SafeAttachmentFilename(pair[0]); got != pair[1] || len(got) > 255 || !utf8.ValidString(got) {
			t.Errorf("%q => %q, want %q", pair[0], got, pair[1])
		}
	}
	for _, pair := range [][2]string{{"案内/図.png", "案内_図.png"}, {`案内\図.png`, "案内_図.png"}, {"../案内.txt", ".._案内.txt"}, {"..", "_"}, {"", "_"}, {"a\n\x00b.txt", "a__b.txt"}, {"CON", "CON_"}, {"COM1.txt", "COM1_.txt"}, {"PRN.tar.gz", "PRN_.tar.gz"}, {"COM¹.pdf", "COM¹_.pdf"}, {"計画.txt. ", "計画.txt"}, {".txt", "_.txt"}, {strings.Repeat("a", 300) + ".pdf", strings.Repeat("a", 251) + ".pdf"}, {strings.Repeat("あ", 100) + ".txt", strings.Repeat("あ", 83) + ".txt"}} {
		if got := SafeAttachmentFilename(pair[0]); got != pair[1] || len(got) > 255 || !utf8.ValidString(got) {
			t.Errorf("%q => %q, want %q", pair[0], got, pair[1])
		}
	}
}

func TestAtomicStreamReplacementAndLockedDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(context.Background(), path, ".wiki-attachment-*.tmp", func(w io.Writer) error { _, e := io.WriteString(w, "new"); return e }, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Fatal(string(data))
	}
	if runtime.GOOS != "windows" {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = WriteAtomic(context.Background(), path, ".wiki-attachment-*.tmp", func(w io.Writer) error { _, e := io.WriteString(w, "replacement"); return e }, nil)
	if err == nil {
		t.Fatal("locked destination was replaced")
	}
	data, _ = os.ReadFile(path)
	entries, _ := os.ReadDir(dir)
	if string(data) != "new" || len(entries) != 1 {
		t.Fatal(string(data), len(entries))
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

type atomicFailureFile struct {
	File
	stage string
	err   error
}

func (f atomicFailureFile) Sync() error {
	if f.stage == "sync" {
		return f.err
	}
	return f.File.Sync()
}
func (f atomicFailureFile) Close() error {
	err := f.File.Close()
	if f.stage == "close" {
		return f.err
	}
	return err
}

func TestAtomicRawWriterPreservesDiskReasons(t *testing.T) {
	for _, stage := range []string{"create", "sync", "close", "rename", "remove"} {
		for _, raw := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "-attachment", true: "-excel"}[raw], func(t *testing.T) {
				failure := &os.PathError{Op: stage, Path: "架空.xlsx", Err: os.ErrPermission}
				ops := DefaultAtomicOps()
				ops.RawWriter = raw
				create := ops.CreateTemp
				ops.CreateTemp = func(dir, pattern string) (File, error) {
					if stage == "create" {
						return nil, failure
					}
					f, err := create(dir, pattern)
					return atomicFailureFile{f, stage, failure}, err
				}
				if stage == "rename" {
					ops.Rename = func(string, string) error { return failure }
				}
				if stage == "remove" {
					ops.Remove = func(string) error { return failure }
				}
				err := WriteAtomicWithOps(context.Background(), filepath.Join(t.TempDir(), "架空.xlsx"), ".tmp-*", func(w io.Writer) error {
					if stage == "remove" {
						return errors.New("書き出し失敗")
					}
					_, err := io.WriteString(w, "data")
					return err
				}, nil, ops)
				if raw {
					if !errors.Is(err, failure) {
						t.Fatalf("OS の理由が失われた: %v", err)
					}
				} else {
					code := "disk"
					if stage == "remove" {
						code = "cleanup_failed"
					}
					if err == nil || err.Error() != code {
						t.Fatalf("固定 code=%s, got %v", code, err)
					}
				}
			})
		}
	}
}
