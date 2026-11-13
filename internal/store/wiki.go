package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

type Wiki struct {
	ID               int64  `json:"id"`
	ProjectID        int64  `json:"projectId"`
	Name             string `json:"name"`
	Content          string `json:"content"`
	TagsJSON         string `json:"tagsJson"`
	Created          string `json:"created"`
	Updated          string `json:"updated"`
	ContentUpdated   string `json:"contentUpdated"`
	RawJSON          string `json:"rawJson"`
	ListFetchedAt    string `json:"listFetchedAt"`
	ContentFetchedAt string `json:"contentFetchedAt"`
	Deleted          bool   `json:"deleted"`
}
type WikiAttachment struct {
	WikiID  int64  `json:"wikiId"`
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created string `json:"created"`
	RawJSON string `json:"rawJson"`
}
type WikiFilter struct {
	ProjectID   int64  `json:"projectId"`
	Keyword     string `json:"keyword"`
	KeywordMode string `json:"keywordMode"`
}

func GetWikiAttachment(ctx context.Context, q dbtx, projectID, wikiID, attachmentID int64) (*WikiAttachment, error) {
	var a WikiAttachment
	err := q.QueryRowContext(ctx, `SELECT a.wiki_id,a.id,a.name,a.size FROM wiki_attachments a JOIN wikis w ON w.id=a.wiki_id JOIN projects p ON p.id=w.project_id WHERE w.project_id=? AND w.id=? AND a.id=? AND w.deleted=0`, projectID, wikiID, attachmentID).Scan(&a.WikiID, &a.ID, &a.Name, &a.Size)
	return &a, err
}

// UpsertWikiList は本文の取得基準を更新しない。一覧失敗後の再試行を保証する。
func UpsertWikiList(ctx context.Context, q dbtx, w *Wiki, at string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO wikis(id,project_id,name,tags_json,created,updated,raw_json,list_fetched_at,search_text) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,tags_json=excluded.tags_json,created=excluded.created,updated=excluded.updated,raw_json=excluded.raw_json,list_fetched_at=excluded.list_fetched_at,deleted=0,search_text=? || char(10) || COALESCE(content,'') WHERE wikis.project_id=excluded.project_id`, w.ID, w.ProjectID, w.Name, w.TagsJSON, w.Created, w.Updated, w.RawJSON, at, NormalizeSearchText(w.Name), NormalizeSearchText(w.Name))
	if err != nil {
		return err
	}
	// 本文側も NFKC・ケースフォールド済みの検索列に揃える。
	var content string
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(content,'') FROM wikis WHERE id=? AND project_id=?`, w.ID, w.ProjectID).Scan(&content); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE wikis SET search_text=? WHERE id=?`, NormalizeSearchText(w.Name+"\n"+content), w.ID)
	return err
}

// SaveWikiContent は呼び出し元の同一 Tx で本文・取得基準・添付を確定する。
func SaveWikiContent(ctx context.Context, q dbtx, id int64, content, updated, at string, attachments []WikiAttachment) error {
	var name string
	if err := q.QueryRowContext(ctx, `SELECT name FROM wikis WHERE id=?`, id).Scan(&name); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `UPDATE wikis SET content=?,content_updated=?,content_fetched_at=?,search_text=? WHERE id=?`, content, updated, at, NormalizeSearchText(name+"\n"+content), id); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM wiki_attachments WHERE wiki_id=?`, id); err != nil {
		return err
	}
	for _, a := range attachments {
		if _, err := q.ExecContext(ctx, `INSERT INTO wiki_attachments(wiki_id,id,name,size,created,raw_json) VALUES(?,?,?,?,?,?)`, id, a.ID, a.Name, a.Size, a.Created, a.RawJSON); err != nil {
			return err
		}
	}
	return nil
}

const wikiColumns = `wikis.id,wikis.project_id,wikis.name,COALESCE(wikis.content,''),wikis.tags_json,wikis.created,wikis.updated,wikis.content_updated,wikis.raw_json,COALESCE(wikis.list_fetched_at,''),COALESCE(wikis.content_fetched_at,''),wikis.deleted`

func scanWiki(row interface{ Scan(...any) error }) (*Wiki, error) {
	var w Wiki
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Content, &w.TagsJSON, &w.Created, &w.Updated, &w.ContentUpdated, &w.RawJSON, &w.ListFetchedAt, &w.ContentFetchedAt, &w.Deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &w, err
}
func GetWiki(ctx context.Context, q dbtx, projectID, id int64) (*Wiki, error) {
	return scanWiki(q.QueryRowContext(ctx, `SELECT `+wikiColumns+` FROM wikis WHERE project_id=? AND id=? AND deleted=0`, projectID, id))
}
func SearchWikis(ctx context.Context, q dbtx, f WikiFilter) ([]Wiki, error) {
	if f.ProjectID <= 0 {
		return nil, errors.New("プロジェクトが指定されていません")
	}
	from := "wikis"
	where := []string{"wikis.project_id=?", "wikis.deleted=0"}
	args := []any{f.ProjectID}
	terms := splitKeywords(f.Keyword)
	or := isOrKeywordMode(f.KeywordMode)
	if expr, ok := ftsMatchExpr(terms, or); ok {
		from = "wikis_fts CROSS JOIN wikis ON wikis.id=wikis_fts.rowid"
		where = append(where, "wikis_fts MATCH ?")
		args = append(args, expr)
	}
	if len(terms) > 0 {
		conditions := []string{}
		for _, term := range terms {
			conditions = append(conditions, `wikis.search_text LIKE ? ESCAPE '\'`)
			args = append(args, "%"+escapeLike(term)+"%")
		}
		op := " AND "
		if or {
			op = " OR "
		}
		where = append(where, "("+strings.Join(conditions, op)+")")
	}
	rows, err := q.QueryContext(ctx, `SELECT `+wikiColumns+` FROM `+from+` WHERE `+strings.Join(where, " AND ")+` ORDER BY wikis.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Wiki{}
	for rows.Next() {
		w, err := scanWiki(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}
func ListWikiAttachments(ctx context.Context, q dbtx, id int64) ([]WikiAttachment, error) {
	rows, err := q.QueryContext(ctx, `SELECT wiki_id,id,name,size,created,raw_json FROM wiki_attachments WHERE wiki_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WikiAttachment{}
	for rows.Next() {
		var a WikiAttachment
		if err := rows.Scan(&a.WikiID, &a.ID, &a.Name, &a.Size, &a.Created, &a.RawJSON); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func MarkWikisMissing(ctx context.Context, q dbtx, projectID int64, ids []int64) error {
	// 全件リストは一時表に格納し、SQLite のバインド変数数の上限を避ける。
	if _, err := q.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS wiki_seen(id INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM wiki_seen`); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := q.ExecContext(ctx, `INSERT INTO wiki_seen(id) VALUES(?)`, id); err != nil {
			return err
		}
	}
	_, err := q.ExecContext(ctx, `UPDATE wikis SET deleted=1 WHERE project_id=? AND id NOT IN (SELECT id FROM wiki_seen)`, projectID)
	return err
}

// WikiDisabled は Wiki API の権限不足を画面再表示後も維持する。
func WikiDisabled(ctx context.Context, q dbtx, projectID int64) (bool, error) {
	v, err := GetMeta(ctx, q, "wiki_disabled_"+strconv.FormatInt(projectID, 10))
	return v == "1", err
}
func SetWikiDisabled(ctx context.Context, q dbtx, projectID int64, disabled bool) error {
	v := "0"
	if disabled {
		v = "1"
	}
	return SetMeta(ctx, q, "wiki_disabled_"+strconv.FormatInt(projectID, 10), v)
}
func GetWikiIncludingDeleted(ctx context.Context, q dbtx, projectID, id int64) (*Wiki, error) {
	return scanWiki(q.QueryRowContext(ctx, `SELECT `+wikiColumns+` FROM wikis WHERE project_id=? AND id=?`, projectID, id))
}
