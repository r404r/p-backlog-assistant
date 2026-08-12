package export

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// listDir はディレクトリ内のファイル名一覧を返す(一時ファイルの残存確認用)。
func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestWriteFileAtomic_CreatesFile は新規ファイルを書き出し、
// 一時ファイルを残さないことを確認する(R5)。
func TestWriteFileAtomic_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "出力.xlsx")

	if err := writeFileAtomic(path, func(w io.Writer) error {
		_, err := io.WriteString(w, "new")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("内容 = %q, want %q", got, "new")
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Errorf("ディレクトリ内 = %v, want 出力ファイルのみ", names)
	}
}

// TestWriteFileAtomic_ReplacesExistingFile は既存ファイルを原子的に置換する
// ことを確認する(R5)。
func TestWriteFileAtomic_ReplacesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "出力.xlsx")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, func(w io.Writer) error {
		_, err := io.WriteString(w, "new")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("内容 = %q, want %q(置換されていない)", got, "new")
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Errorf("ディレクトリ内 = %v, want 出力ファイルのみ", names)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("権限 = %o, want 600", perm)
		}
	}
}

// TestWriteFileAtomic_KeepsExistingFileOnFailure は書き出し途中で失敗しても
// 既存ファイルを失わず、一時ファイルも残さないことを確認する(R5)。
// 従来は os.Create が選択済みファイルを即時切り詰めていたため、
// 途中で失敗すると出力先の既存ファイルを失っていた。
func TestWriteFileAtomic_KeepsExistingFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "出力.xlsx")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("フェイク: 書き出し失敗")
	err := writeFileAtomic(path, func(w io.Writer) error {
		// 途中まで書いてから失敗する(部分的な内容が残らないこと)
		if _, err := io.WriteString(w, "partial"); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("エラー = %v, want %v", err, wantErr)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Errorf("既存ファイルの内容 = %q, want %q(失敗で失われた)", got, "old")
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Errorf("ディレクトリ内 = %v, want 既存ファイルのみ(一時ファイルが残っている)", names)
	}
}

// TestWriteFileAtomic_RemovesTempOnRenameFailure は置換自体に失敗した場合でも
// 一時ファイルを残さないことを確認する(R5)。
// 出力先にディレクトリを指定するとリネームが失敗する。
func TestWriteFileAtomic_RemovesTempOnRenameFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "サブ")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(target, func(w io.Writer) error {
		_, err := io.WriteString(w, "new")
		return err
	}); err == nil {
		t.Fatal("ディレクトリへの書き出しが成功してしまった")
	}
	names := listDir(t, dir)
	if len(names) != 1 || names[0] != "サブ" {
		t.Errorf("ディレクトリ内 = %v, want [サブ](一時ファイルが残っている)", names)
	}
}

// TestExportToFile_ReplacesExistingFileWithoutLeftovers は 4 つの Excel 出力
// (課題・ユーザ・一括テンプレート・実行結果)がいずれも既存ファイルを
// 置換し、一時ファイルを残さないことを確認する(R5)。
func TestExportToFile_ReplacesExistingFileWithoutLeftovers(t *testing.T) {
	cases := []struct {
		name   string
		export func(path string) error
	}{
		{"課題", func(path string) error {
			return ExportIssuesToFile(path, IssueSlice(sampleIssues()), Options{})
		}},
		{"ユーザ", func(path string) error {
			return ExportUsersToFile(path, sampleUsers(), UserOptions{})
		}},
		{"一括テンプレート", func(path string) error {
			return ExportBulkTemplateToFile(path, testTemplateProjectID, BulkTemplateSlice(sampleBulkRows()), sampleBulkMasters())
		}},
		{"実行結果", func(path string) error {
			return ExportBulkResultToFile(path, sampleBulkResultRows())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "出力.xlsx")
			// 上書き対象の既存ファイル(xlsx ではない内容)
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := tc.export(path); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// xlsx は ZIP なので "PK" で始まる
			if !strings.HasPrefix(string(got), "PK") {
				t.Errorf("出力が xlsx になっていない(先頭 = %q)", string(got[:min(4, len(got))]))
			}
			if names := listDir(t, dir); len(names) != 1 {
				t.Errorf("ディレクトリ内 = %v, want 出力ファイルのみ(一時ファイルが残っている)", names)
			}
		})
	}
}

// TestExportToFile_KeepsExistingFileOnValidationError は列指定が不正で
// 出力に失敗した場合、出力先の既存ファイルを壊さないことを確認する(R5)。
func TestExportToFile_KeepsExistingFileOnValidationError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "出力.xlsx")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ExportIssuesToFile(path, IssueSlice(sampleIssues()), Options{Columns: []string{"nosuchcolumn"}}); err == nil {
		t.Fatal("未知の列でエラーにならなかった")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Errorf("既存ファイルの内容 = %q, want %q", got, "old")
	}
	if names := listDir(t, dir); len(names) != 1 {
		t.Errorf("ディレクトリ内 = %v, want 既存ファイルのみ", names)
	}
}

// TestWriteFileAtomic_TempFileIsHiddenNeighbor は一時ファイルを出力先と同じ
// ディレクトリに作ることを確認する(R5)。別ボリュームの一時領域を使うと
// リネームが原子的にならないため、隣に置く必要がある。
func TestWriteFileAtomic_TempFileIsHiddenNeighbor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "出力.xlsx")

	var tempDir, tempName string
	if err := writeFileAtomic(path, func(w io.Writer) error {
		f, ok := w.(*os.File)
		if !ok {
			t.Fatalf("書き出し先 = %T, want *os.File", w)
		}
		tempDir, tempName = filepath.Split(f.Name())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(tempDir) != filepath.Clean(dir) {
		t.Errorf("一時ファイルのディレクトリ = %q, want %q", tempDir, dir)
	}
	if !strings.HasPrefix(tempName, ".出力.xlsx") {
		t.Errorf("一時ファイル名 = %q, want 出力先名を含む隠しファイル", tempName)
	}
	if names := listDir(t, dir); len(names) != 1 || names[0] != "出力.xlsx" {
		t.Errorf("ディレクトリ内 = %v, want [出力.xlsx]", names)
	}
}
