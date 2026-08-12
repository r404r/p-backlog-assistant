package export

// columns.go は「画面の列選択に出す列」のメタデータを供給する(R14)。
//
// 列キー・表示ラベル・既定の選択状態は issue.go / user.go の列定義から組み立てる。
// 以前はフロントエンドが同じ一覧をハードコードしており、ラベルが Excel のヘッダと
// ずれていた(画面「作成日」/ Excel「作成日時」)。画面はこのメタデータを表示する
// だけにして、列の追加・改名を Go 側の 1 か所で完結させる。
//
// カスタム属性列(cf_{定義ID})はここには含めない。画面は絞り込み UI のために
// カスタム属性の定義を別途取得しており、その定義から CustomColumnKey と定義名で
// 列を組み立てられる(同じ定義を 2 回取得しないため)。

// ColumnMeta は列選択 UI へ供給する 1 列ぶんのメタデータ。
type ColumnMeta struct {
	// Key は出力時に指定する列キー(ExportIssuesExcel / ExportUsersExcel の columns)。
	Key string `json:"key"`
	// Label は画面に表示するラベル。Excel のヘッダと同一の文字列を返す
	// (画面で選んだ列名と出力されるヘッダが食い違わないようにするため)。
	Label string `json:"label"`
	// ByDefault は既定で選択する列かどうか。
	ByDefault bool `json:"byDefault"`
}

// IssuePickerColumns は課題抽出の列選択に出す列を表示順で返す。
//
// pickerHidden の列(詳細)は含めない。出力自体は列キーを直接指定すれば可能で、
// このフラグは画面への供給だけを制御する。
func IssuePickerColumns() []ColumnMeta {
	defaults := make(map[string]bool, len(defaultColumnKeys))
	for _, k := range defaultColumnKeys {
		defaults[k] = true
	}
	out := make([]ColumnMeta, 0, len(columns))
	for _, c := range columns {
		if c.pickerHidden {
			continue
		}
		out = append(out, ColumnMeta{Key: c.key, Label: c.header, ByDefault: defaults[c.key]})
	}
	return out
}

// UserPickerColumns はユーザ抽出の列選択に出す列を表示順で返す。
func UserPickerColumns() []ColumnMeta {
	out := make([]ColumnMeta, 0, len(userColumns))
	for _, c := range userColumns {
		out = append(out, ColumnMeta{Key: c.key, Label: c.header, ByDefault: !c.optional})
	}
	return out
}
