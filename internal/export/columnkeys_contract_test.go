package export

import "testing"

// columnkeys_contract_test.go は「固定列の列キー集合」を Go 側にも明示する契約テスト。
//
// 多言語対応(リサーチ「backlog-assistant-多言語対応-2026-08」§3.3)では、
// フロントエンドが固定列 key を辞書で翻訳する。Go 側に列を足したのに
// フロントの翻訳対応表へ追加し忘れると、英語 UI に日本語ラベルが残る。
// これを検知するため、対応表の網羅検査を Go / フロントの両側に置く:
//
//   - フロント側: frontend/src/lib/columnLabels.test.ts が issue.go の columns と
//     user.go の userColumns から列キーを抽出し、翻訳対応表と完全一致を検査する。
//   - Go 側(このファイル): 列キー集合をテストへ明示的に書き下し、列の追加・改名を
//     「テストの更新」という形で必ず意識させる。
//
// 列を追加・改名したときは、このテストとフロントの対応表
// (frontend/src/lib/columnLabels.ts)・カタログ(locales/{ja,en}/common.json)を
// 同一タスク内で更新すること。

// TestIssueColumnKeys_Contract は課題出力の固定列キーを表示順で固定する。
// pickerHidden の列(詳細)も列キーとしては指定できるため含める。
func TestIssueColumnKeys_Contract(t *testing.T) {
	want := []string{
		"issueKey",
		"summary",
		"statusName",
		"assigneeName",
		"issueTypeName",
		"priorityName",
		"created",
		"updated",
		"dueDate",
		"description",
		"parentIssueKey",
	}

	got := make([]string, 0, len(columns))
	for _, c := range columns {
		got = append(got, c.key)
	}

	assertKeys(t, "課題", got, want)
}

// TestUserColumnKeys_Contract はユーザ出力の固定列キーを表示順で固定する。
func TestUserColumnKeys_Contract(t *testing.T) {
	want := []string{
		"userCode",
		"name",
		"mailAddress",
		"roleName",
		"roleType",
		"teamNames",
		"projectKeys",
		"adminProjectKeys",
	}

	got := make([]string, 0, len(userColumns))
	for _, c := range userColumns {
		got = append(got, c.key)
	}

	assertKeys(t, "ユーザ", got, want)
}

// TestIssueColumnKeys_PickerHidden は、列選択に出さない列(pickerHidden)が
// どれかを固定する。フロントの抽出はこのフラグを考慮するため、増減に気付けるようにする。
func TestIssueColumnKeys_PickerHidden(t *testing.T) {
	want := []string{"description"}

	var got []string
	for _, c := range columns {
		if c.pickerHidden {
			got = append(got, c.key)
		}
	}

	assertKeys(t, "課題(列選択に出さない)", got, want)
}

// assertKeys は列キーの並びが期待どおりかを確認する。
func assertKeys(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s列のキー = %v, want %v", name, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s列 %d 番目のキー = %q, want %q", name, i, got[i], want[i])
		}
	}
}
