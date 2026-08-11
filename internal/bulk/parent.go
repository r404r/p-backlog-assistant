package bulk

// parent.go は一括更新・追加における親課題(CF5)の入力検証・差分判定・
// 送信値の組み立てを担う。
//
// 入力規約(記入方法シートと同じ内容。食い違わせないこと):
//   - 列ヘッダは「親課題キー」(固定列の末尾グループ。カスタム属性列より前)。
//   - 値は課題キー(EXA-1)または ID:<数値>(ローカルに無い親)。
//   - 空セル = 変更しない、#CLEAR# = 親子関係の解除(実機検証中)。
//   - 親に指定できるのは既存課題だけ(同一取り込み内の新規追加行は課題キーが
//     未採番のため親にできない)。
//
// Backlog の親子関係は 1 階層までのため、次の 4 点を検証する(計画 CF5):
//
//	(a) 親に指定する課題が親を持たない(孫の禁止)
//	(b) 親を設定する課題自身が子を持たない(2 階層化の禁止)
//	(c) 自己参照の禁止
//	(d) 同一バッチ内の組み合わせで 2 階層が生じない
//
// (a)〜(c) は取り込み時のローカル DB の状態に対して行ごとに判定し、
// (d) は全行の変更を適用した後の関係に対してまとめて判定する。
//
// ローカルの生 JSON から親の有無を判定できない課題(旧バージョンで同期した
// 課題)は「親なし」とはみなさない。潰してしまうと (a) の孫指定を許可し、
// (b) の子を見落とし、(c) 以前に #CLEAR# が「変更なし」で黙殺される。
// 判定できない課題が関わる行は行エラーにし、フル同期を案内する
// (検証できないまま送信しない)。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/export"
	"backlog-assistant/internal/store"
)

// parentLabel は差分表示・エラーメッセージで使う項目名。
const parentLabel = "親課題"

// fatalError は行エラーではなく取り込み全体を止めるべき失敗。
//
// 行の内容が原因ではない失敗(認証・レート制限・通信障害・中断)を行エラーに
// すると、利用者は「その行の指定が誤っている」と受け取ってしまう。しかも
// 後続の行も同じ理由で失敗するため、全行分のエラーが並ぶ。
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// isFatal は取り込み全体を止めるべき失敗かを返す。
func isFatal(err error) bool {
	var f *fatalError
	return errors.As(err, &f)
}

// parentIndex はプロジェクト内の親子関係の索引。
//
// 「子を持つか」の判定はプロジェクトの全課題を見ないと決められないため、
// 取り込みの入口で 1 回だけローカル DB を走査して作り、全行で使い回す
// (行ごとに問い合わせると課題数 × 行数のコストになる)。
// 親課題キー列が無いファイルでは作らない(走査コストを掛けない)。
type parentIndex struct {
	// keyByID は課題 ID → 課題キー(表示用)。
	keyByID map[int64]string
	// idByKey は正規化した課題キー → 課題 ID(入力の解決用)。
	idByKey map[string]int64
	// parentOf は課題 ID → 親課題 ID(親なし・判定不能は登録しない)。
	parentOf map[int64]int64
	// hasChild は子を持つ課題 ID の集合。
	hasChild map[int64]bool
	// unverifiable は親の有無を判定できない課題 ID の集合。
	// 1 件でもあると「子を持たないこと」を断言できない(その課題が子である
	// 可能性を否定できない)ため、親を新たに設定する更新行は受理しない。
	unverifiable map[int64]bool
	// remoteChecked はローカルに無い親(ID:<数値>)を API で確認した結果の
	// キャッシュ(値が nil なら親として使える)。同じ親を指す行が複数あっても
	// API 呼び出しを 1 回に抑える。
	remoteChecked map[int64]error
}

// newParentIndex はローカル DB の課題一覧から索引を作る。
func newParentIndex(rows []store.IssueParent) *parentIndex {
	idx := &parentIndex{
		keyByID:       make(map[int64]string, len(rows)),
		idByKey:       make(map[string]int64, len(rows)),
		parentOf:      map[int64]int64{},
		hasChild:      map[int64]bool{},
		unverifiable:  map[int64]bool{},
		remoteChecked: map[int64]error{},
	}
	for _, r := range rows {
		idx.keyByID[r.ID] = r.IssueKey
		if key := normalizeHeader(r.IssueKey); key != "" {
			idx.idByKey[key] = r.ID
		}
		switch r.State {
		case store.ParentSet:
			idx.parentOf[r.ID] = r.ParentIssueID
			idx.hasChild[r.ParentIssueID] = true
		case store.ParentUnknown:
			idx.unverifiable[r.ID] = true
		}
	}
	return idx
}

// syncHint は親子関係を判定できない場合に添える案内。
const syncHint = "対象プロジェクトをフル同期してから再実行してください"

// label は課題 ID の表示名(ローカルに無い課題は ID:<数値>)。
func (p *parentIndex) label(id int64) string {
	return export.FormatParentIssueRef(id, p.keyByID)
}

// parentChange は 1 行の親課題の変更内容(バッチ検証 (d) で使う)。
type parentChange struct {
	// issueID は更新行の課題 ID。新規追加行は課題 ID が未採番のため 0。
	issueID int64
	// label はエラーメッセージで使う対象の表記。
	label string
	// parentID は設定する親課題 ID(clear が真なら意味を持たない)。
	parentID int64
	// clear は親子関係の解除。
	clear bool
}

// planParent は行の親課題キー列を検証し、差分・送信値を組み立てる。
//
// cur は更新行の現在値(新規追加行は nil)。呼び出し側は親課題キー列を持つ
// ファイルでのみ呼ぶこと(v.parents が nil の場合は何もしない)。
func (v *validator) planParent(ctx context.Context, r rawRow, cur *store.Issue, plan *rowPlan) error {
	if v.parents == nil {
		return nil // 親課題キー列が無いファイル(旧テンプレート)
	}
	raw := r.cell(colParentIssueKey)
	if raw == "" {
		return nil // 空欄 = 変更しない
	}
	if cur == nil {
		return v.planParentCreate(ctx, raw, plan)
	}
	return v.planParentUpdate(ctx, raw, cur, plan)
}

// planParentCreate は新規追加行の親課題を検証する。
// 新規追加行では #CLEAR# を使えない(解除すべき既存の親が無い)。
func (v *validator) planParentCreate(ctx context.Context, raw string, plan *rowPlan) error {
	if raw == ClearToken {
		return fmt.Errorf("新規追加行では %s を指定できません", ClearToken)
	}
	parentID, err := v.resolveParentRef(raw)
	if err != nil {
		return err
	}
	// 新規追加する課題自身は子を持たないため、(a) だけを確認する
	if err := v.verifyParentCandidate(ctx, parentID); err != nil {
		return err
	}
	plan.payload.ParentIssueID = ptrInt64(parentID)
	plan.changes = append(plan.changes, fmt.Sprintf("%s: %s", parentLabel, v.parents.label(parentID)))
	plan.parent = &parentChange{
		label:    fmt.Sprintf("%d 行目の新規課題", plan.rowNo),
		parentID: parentID,
	}
	return nil
}

// planParentUpdate は更新行の親課題を検証し、現在値と異なる場合だけ送信値に載せる。
func (v *validator) planParentUpdate(ctx context.Context, raw string, cur *store.Issue, plan *rowPlan) error {
	// 現在値は生 JSON から読む(列を持たないため。store.ParentIssueRef のコメント参照)。
	// 判定できない課題は差分を取れないため行エラーにする(「親なし」と誤認すると、
	// 実際は親が居るのに #CLEAR# が「変更なし」で黙殺される)。
	curParent, curState := store.ParentIssueRef(cur.RawJSON)
	if curState == store.ParentUnknown {
		return fmt.Errorf("課題 %s の現在の親課題を判定できません(ローカルデータが古い可能性があります。%s)",
			cur.IssueKey, syncHint)
	}

	if raw == ClearToken {
		if curParent == 0 {
			return nil // 既に親なし(空の送信をしない)
		}
		plan.payload.ClearParentIssue = true
		plan.changes = append(plan.changes, change(parentLabel, v.parents.label(curParent), "(クリア)"))
		plan.parent = &parentChange{issueID: cur.ID, label: cur.IssueKey, clear: true}
		return nil
	}

	// 解決だけ先に行い、現在値と同じ行はそこで打ち切る。プリフィルされた値を
	// 未編集のまま取り込んだ行で API 確認(ID:<数値> の親の状態確認)を
	// 走らせないため。
	parentID, err := v.resolveParentRef(raw)
	if err != nil {
		return err
	}
	if parentID == curParent {
		return nil // 変更なし
	}
	if parentID == cur.ID {
		return fmt.Errorf("課題 %s を自分自身の親に指定できません", cur.IssueKey)
	}
	// (b) 子を持つ課題は別の課題の子にできない(2 階層化の禁止)
	if v.parents.hasChild[cur.ID] {
		return fmt.Errorf("課題 %s は子課題を持っているため、別の課題の子にできません(Backlog の親子関係は 1 階層までです)",
			cur.IssueKey)
	}
	// (a) 親に指定する課題が親を持たない(孫の禁止)。
	// 判定できない課題の件数より、指定した親そのものの問題を先に知らせる
	//(利用者が直せる指定の誤りを優先して報告する)。
	if err := v.verifyParentCandidate(ctx, parentID); err != nil {
		return err
	}
	// 親の有無を判定できない課題が残っている間は「子を持たない」と断言できない
	//(その課題がこの行の子である可能性を否定できない)
	if n := len(v.parents.unverifiable); n > 0 {
		return fmt.Errorf("親子関係を判定できない課題が %d 件あるため、課題 %s に親課題を設定できません"+
			"(子課題を持っていないか確認できません。%s)", n, cur.IssueKey, syncHint)
	}
	plan.payload.ParentIssueID = ptrInt64(parentID)
	plan.changes = append(plan.changes,
		change(parentLabel, v.parents.label(curParent), v.parents.label(parentID)))
	plan.parent = &parentChange{issueID: cur.ID, label: cur.IssueKey, parentID: parentID}
	return nil
}

// resolveParentRef はセルの値を親課題 ID へ解決する(API は呼ばない)。
//
// 課題キーはローカル DB で解決する。同一取り込み内の新規追加行は課題キーが
// 未採番のため、ここで「見つからない」として弾かれる(初回スコープでは
// 新規行どうしの親子に対応しない)。
func (v *validator) resolveParentRef(raw string) (int64, error) {
	// 「ID:」で始まる値は課題キーとして解釈し直さない(ID:0・ID:abc が
	// 「そんな課題キーは無い」という的外れなエラーになるのを防ぐ)
	if id, matched := export.ParseParentIssueIDRef(raw); matched {
		if id <= 0 {
			return 0, fmt.Errorf("親課題「%s」の %s<数値> 形式が不正です(%s123 のように正の課題 ID を指定してください)",
				raw, export.ParentIssueIDPrefix, export.ParentIssueIDPrefix)
		}
		return id, nil
	}
	if id, ok := v.parents.idByKey[normalizeHeader(raw)]; ok {
		return id, nil
	}
	return 0, fmt.Errorf("親課題「%s」がローカルデータに見つかりません(対象プロジェクトを同期してから再実行してください。"+
		"同じファイル内で追加する新規課題を親に指定することはできません)", raw)
}

// verifyParentCandidate は親に指定された課題が (a) を満たすかを確認する。
//
// ローカルにある課題は索引で判定する。ローカルに無い課題(ID:<数値> 指定)は
// 課題取得 API で状態を確認する。確認できない場合(API 未設定・削除済み・
// 権限外)は行エラーにし、検証できないまま送信しない(計画 CF5)。
// 親が別プロジェクトの課題である場合も、本ツールの初回スコープ外として拒否する。
func (v *validator) verifyParentCandidate(ctx context.Context, parentID int64) error {
	if _, local := v.parents.keyByID[parentID]; local {
		if v.parents.unverifiable[parentID] {
			return fmt.Errorf("親に指定した課題 %s の親子関係を判定できません"+
				"(その課題自身が子課題でないか確認できません。%s)", v.parents.label(parentID), syncHint)
		}
		if grand, ok := v.parents.parentOf[parentID]; ok {
			return fmt.Errorf("親に指定した課題 %s は別の課題(%s)の子課題です(Backlog の親子関係は 1 階層までです)",
				v.parents.label(parentID), v.parents.label(grand))
		}
		return nil
	}
	return v.verifyRemoteParent(ctx, parentID)
}

// verifyRemoteParent はローカルに無い親を API で確認する。
//
// 確定的な結果(親の状態・404 / 403 の行エラー)だけをキャッシュし、同じ親を
// 指す行が何行あっても API 呼び出しを 1 回に抑える。取り込み全体を止める失敗
// (認証・レート制限・通信障害・中断)はキャッシュしない。
func (v *validator) verifyRemoteParent(ctx context.Context, parentID int64) error {
	if err, done := v.parents.remoteChecked[parentID]; done {
		return err
	}
	err := v.fetchRemoteParent(ctx, parentID)
	if isFatal(err) {
		return err
	}
	v.parents.remoteChecked[parentID] = err
	return err
}

// fetchRemoteParent は課題取得 API で親の状態を確認する。
//
// 失敗の扱いは 2 分類する:
//   - 404 / 403: 対象の課題が存在しない・見えないことが確定しており、他の行には
//     影響しない。行エラーにする(取り込み全体は止めない)。
//   - それ以外(認証・レート制限・通信障害・5xx・中断): 親の状態を確認できて
//     いないだけで、指定自体が誤っているとは限らない。行エラーにすると
//     「親が居ないから通らなかった」と誤解させるうえ、後続の行も同じ理由で
//     失敗するため、取り込み全体を中断する。
func (v *validator) fetchRemoteParent(ctx context.Context, parentID int64) error {
	ref := export.ParentIssueIDPrefix + strconv.FormatInt(parentID, 10)
	if v.api == nil {
		return fmt.Errorf("親課題 %s はローカルデータに無いため状態を確認できません(%s)", ref, syncHint)
	}
	issue, err := v.api.GetIssue(ctx, strconv.FormatInt(parentID, 10))
	if err != nil {
		if !errors.Is(err, backlogclient.ErrNotFound) && !errors.Is(err, backlogclient.ErrPermissionDenied) {
			return &fatalError{err: fmt.Errorf("親課題 %s の状態を確認できませんでした: %w", ref, err)}
		}
		return fmt.Errorf("親課題 %s の状態を確認できません(削除済み・権限が無い可能性があります): %w", ref, err)
	}
	if issue.ProjectID != v.projectID {
		return fmt.Errorf("親課題 %s は対象プロジェクト外の課題です(他プロジェクトの課題を親にする操作には対応していません)", ref)
	}
	if issue.ParentIssueID > 0 {
		return fmt.Errorf("親に指定した課題 %s は別の課題の子課題です(Backlog の親子関係は 1 階層までです)", ref)
	}
	return nil
}

// validateBatch は全行の変更を適用した後の親子関係を検証する((d))。
//
// 行ごとの検証(planParent)は取り込み時点のローカル DB の状態しか見ないため、
// 「A の親を B にする」行と「B の親を C にする」行が同居すると 2 階層になる。
// ここでは変更適用後の関係を組み立て直し、違反する行を洗い出す。
//
// 新規追加行は課題 ID が未採番のため、行番号から作る負の仮 ID で表す
// (実在の課題 ID は正のため衝突しない)。これにより「新規行の親に指定した
// 課題へ、別の行が親を設定する」組み合わせも検出できる。
//
// 戻り値は違反した行のエラー(行番号昇順)。
func (p *parentIndex) validateBatch(plans []*rowPlan) []RowError {
	changed := make([]*rowPlan, 0, len(plans))
	for _, plan := range plans {
		if plan.parent != nil {
			changed = append(changed, plan)
		}
	}
	if len(changed) == 0 {
		return nil
	}

	// 変更適用後の「課題 ID → 親課題 ID」(ローカル DB の状態を土台にする)
	effective := make(map[int64]int64, len(p.parentOf)+len(changed))
	for id, parent := range p.parentOf {
		effective[id] = parent
	}
	for _, plan := range changed {
		id := batchIssueID(plan)
		if plan.parent.clear {
			delete(effective, id)
			continue
		}
		effective[id] = plan.parent.parentID
	}
	// 変更適用後に子を持つ課題
	hasChild := make(map[int64]bool, len(effective))
	for _, parent := range effective {
		hasChild[parent] = true
	}

	errs := []RowError{}
	for _, plan := range changed {
		if plan.parent.clear {
			continue // 解除は階層を深くしない
		}
		switch {
		case effective[plan.parent.parentID] != 0:
			errs = append(errs, RowError{
				RowNo: plan.rowNo,
				Message: fmt.Sprintf("同じ取り込み内の指定を適用すると親子が 2 階層になります"+
					"(%s の親に指定した課題が、別の課題の子になります)", plan.parent.label),
			})
		case hasChild[batchIssueID(plan)]:
			errs = append(errs, RowError{
				RowNo: plan.rowNo,
				Message: fmt.Sprintf("同じ取り込み内の指定を適用すると親子が 2 階層になります"+
					"(%s が子課題を持つ状態になるため、別の課題の子にできません)", plan.parent.label),
			})
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].RowNo < errs[j].RowNo })
	return errs
}

// batchIssueID はバッチ検証で行を表す課題 ID を返す
// (新規追加行は未採番のため行番号から作る負の仮 ID)。
func batchIssueID(plan *rowPlan) int64 {
	if plan.parent.issueID != 0 {
		return plan.parent.issueID
	}
	return -int64(plan.rowNo)
}
