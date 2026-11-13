package export

import (
	"backlog-assistant/internal/fileutil"
	"context"
	"io"
	"path/filepath"
)

// 同一ディレクトリの一時ファイルへ書き切ってから os.Rename で置換する。
// 非 Unix 環境や電源断を含む原子性は保証しない。失敗時に既存ファイルは削除しない。
func writeFileAtomic(path string, write func(io.Writer) error) error {
	ops := fileutil.DefaultAtomicOps()
	ops.RawWriter = true
	return fileutil.WriteAtomicWithOps(context.Background(), path, "."+filepath.Base(path)+".tmp", write, nil, ops)
}
