package fileutil

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Error は保存先や元の OS エラーを公開しない。
type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

type File interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}
type AtomicOps struct {
	// 既存 Excel callback の *os.File と OS エラーの契約を保持する場合だけ使用する。
	RawWriter  bool
	CreateTemp func(string, string) (File, error)
	Rename     func(string, string) error
	Remove     func(string) error
}

func DefaultAtomicOps() AtomicOps {
	return AtomicOps{
		CreateTemp: func(dir, pattern string) (File, error) { return os.CreateTemp(dir, pattern) }, Rename: os.Rename, Remove: os.Remove,
	}
}

// publish は取消と公開の境界を呼び出し側の状態ロックで直列化する。
func WriteAtomic(ctx context.Context, path, pattern string, write func(io.Writer) error, publish func(func() error) error) error {
	return WriteAtomicWithOps(ctx, path, pattern, write, publish, DefaultAtomicOps())
}
func WriteAtomicWithOps(ctx context.Context, path, pattern string, write func(io.Writer) error, publish func(func() error) error, ops AtomicOps) (err error) {
	diskError := func(cause error) error {
		if ops.RawWriter {
			return cause
		}
		return &Error{"disk"}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f, e := ops.CreateTemp(filepath.Dir(path), pattern)
	if e != nil {
		return diskError(e)
	}
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if !committed {
			if e := ops.Remove(f.Name()); e != nil && !errors.Is(e, os.ErrNotExist) {
				if ops.RawWriter {
					err = errors.Join(err, e)
				} else {
					err = &Error{"cleanup_failed"}
				}
			}
		}
	}()
	writer := &checkedWriter{ctx: ctx, dst: f}
	var dst io.Writer = writer
	if ops.RawWriter {
		dst = f
	}
	if err = write(dst); writer.failed {
		return &Error{"disk"}
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if e = f.Sync(); e != nil {
		return diskError(e)
	}
	e = f.Close()
	closed = true
	if e != nil {
		return diskError(e)
	}
	rename := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e := ops.Rename(f.Name(), path); e != nil {
			return diskError(e)
		}
		return nil
	}
	if publish != nil {
		err = publish(rename)
	} else {
		err = rename()
	}
	committed = err == nil
	return err
}

type checkedWriter struct {
	ctx    context.Context
	dst    io.Writer
	failed bool
}

func (w *checkedWriter) Write(p []byte) (int, error) {
	if w.ctx.Err() != nil {
		return 0, w.ctx.Err()
	}
	n, e := w.dst.Write(p)
	if e == nil && n != len(p) {
		e = io.ErrShortWrite
	}
	if e != nil {
		w.failed = true
	}
	return n, e
}
