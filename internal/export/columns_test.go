package export

import "testing"

// TestIssuePickerColumns_MatchesExcelHeaders は、画面の列選択へ供給するラベルが
// Excel のヘッダと完全に一致することを確認する(R14)。
// 以前はフロントエンドが同じ一覧をハードコードしており、
// 「作成日」(画面)と「作成日時」(Excel)のようなずれが生まれていた。
func TestIssuePickerColumns_MatchesExcelHeaders(t *testing.T) {
	cols := IssuePickerColumns()
	if len(cols) == 0 {
		t.Fatal("列が 1 つも返っていません")
	}
	for _, c := range cols {
		header, ok := ColumnHeader(c.Key, nil)
		if !ok {
			t.Errorf("列キー %q が Excel 出力で解決できません", c.Key)
			continue
		}
		if c.Label != header {
			t.Errorf("列 %q のラベル = %q, Excel ヘッダ = %q", c.Key, c.Label, header)
		}
	}
}

// TestIssuePickerColumns_Defaults は既定で選択する列が DefaultColumns と一致し、
// 親課題キーが既定では選択されないことを確認する。
func TestIssuePickerColumns_Defaults(t *testing.T) {
	var got []string
	for _, c := range IssuePickerColumns() {
		if c.ByDefault {
			got = append(got, c.Key)
		}
	}
	want := DefaultColumns()
	if len(got) != len(want) {
		t.Fatalf("既定列 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("既定列 = %v, want %v", got, want)
		}
	}
	for _, c := range IssuePickerColumns() {
		if c.Key == ParentIssueKeyColumn && c.ByDefault {
			t.Error("親課題キーが既定で選択されています")
		}
	}
}

// TestIssuePickerColumns_HidesDescription は、詳細列が列選択に出ないことを確認する。
// 出力自体は列キーを直接指定すれば従来どおり可能(pickerHidden は画面への供給のみを制御する)。
func TestIssuePickerColumns_HidesDescription(t *testing.T) {
	for _, c := range IssuePickerColumns() {
		if c.Key == "description" {
			t.Error("詳細列が列選択に含まれています")
		}
	}
	if _, ok := ColumnHeader("description", nil); !ok {
		t.Error("詳細列が出力から失われています")
	}
}

// TestUserPickerColumns はユーザ抽出の列選択メタデータを確認する。
// ラベルは Excel ヘッダと一致し、既定選択は DefaultUserColumns と一致する。
func TestUserPickerColumns(t *testing.T) {
	cols := UserPickerColumns()
	if len(cols) != len(AvailableUserColumns()) {
		t.Fatalf("列数 = %d, want %d", len(cols), len(AvailableUserColumns()))
	}
	var defaults []string
	for _, c := range cols {
		header, ok := UserColumnHeader(c.Key)
		if !ok {
			t.Errorf("列キー %q が Excel 出力で解決できません", c.Key)
			continue
		}
		if c.Label != header {
			t.Errorf("列 %q のラベル = %q, Excel ヘッダ = %q", c.Key, c.Label, header)
		}
		if c.ByDefault {
			defaults = append(defaults, c.Key)
		}
	}
	want := DefaultUserColumns()
	if len(defaults) != len(want) {
		t.Fatalf("既定列 = %v, want %v", defaults, want)
	}
	for i := range want {
		if defaults[i] != want[i] {
			t.Fatalf("既定列 = %v, want %v", defaults, want)
		}
	}
	// ロール値(roleType)は選択可能だが既定では出力しない
	for _, c := range cols {
		if c.Key == "roleType" && c.ByDefault {
			t.Error("ロール値が既定で選択されています")
		}
	}
}
