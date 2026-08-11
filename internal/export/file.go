package export

import (
	"io"
	"os"
	"path/filepath"
)

// writeFileAtomic は path へ「同一ディレクトリの一時ファイルに書き切ってから
// リネームで置換する」方式で書き出す(R5)。
//
// 目的は出力失敗で既存ファイルを失わないこと。os.Create は選択されたファイルを
// 開いた時点で切り詰めてしまうため、途中で失敗すると(たとえ後片付けで削除しても)
// 利用者が上書き先に選んだ既存ファイルの内容が失われる。書き込み先を一時ファイルに
// 逃がしておけば、失敗時に消すのは一時ファイルだけで済む。
//
// 実装上の約束:
//   - 一時ファイルは出力先と同じディレクトリに作る。別ボリューム(OS の一時領域)
//     だとリネームがコピーになり、原子的な置換にならないため。
//   - 名前は ".<出力ファイル名>.tmp<乱数>"。隠しファイル扱いにして、
//     万一残っても利用者の一覧を汚さないようにする。
//   - 権限は os.CreateTemp の既定(0600)。出力内容は課題データを含むため、
//     他ユーザから読める権限で作らない(置換後のファイルも 0600 になる)。
//   - os.Rename は Windows でも既存ファイルを置換する(内部で MOVEFILE_REPLACE_EXISTING
//     を使う)。ただし出力先が Excel 等で開かれている場合は失敗するため、
//     その場合はエラーを返し、既存ファイルには手を付けない。
func writeFileAtomic(path string, write func(w io.Writer) error) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// 成功時以外は一時ファイルを必ず片付ける(出力先には触れない)。
	defer func() {
		if err != nil {
			tmp.Close() // 二重 Close は無害(既に閉じていればエラーになるだけ)
			os.Remove(tmpName)
		}
	}()

	if err = write(tmp); err != nil {
		return err
	}
	// Close の失敗(遅延したフラッシュエラー等)も書き出し失敗として扱う。
	if err = tmp.Close(); err != nil {
		return err
	}
	err = os.Rename(tmpName, path)
	return err
}
