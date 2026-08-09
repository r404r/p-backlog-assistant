package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestManager_LoadEmptyWhenMissing(t *testing.T) {
	m := NewManagerAt(t.TempDir())
	cfg, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 0 {
		t.Errorf("空ディレクトリからの Load = %v, want 空", cfg.Profiles)
	}
}

func TestManager_UpsertAndDelete(t *testing.T) {
	m := NewManagerAt(t.TempDir())

	p1 := Profile{ID: "id1", Name: "検証用", SpaceURL: "https://example.backlog.jp"}
	p2 := Profile{ID: "id2", Name: "サブ", SpaceURL: "https://example2.backlog.com"}
	if err := m.Upsert(p1); err != nil {
		t.Fatal(err)
	}
	if err := m.Upsert(p2); err != nil {
		t.Fatal(err)
	}

	// 上書き
	p1.Name = "検証用(改名)"
	if err := m.Upsert(p1); err != nil {
		t.Fatal(err)
	}

	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("プロファイル数 = %d, want 2", len(list))
	}
	got, err := m.Get("id1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "検証用(改名)" {
		t.Errorf("Upsert による上書きが反映されていない: %q", got.Name)
	}

	if err := m.Delete("id1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get("id1"); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("削除後の Get のエラー = %v, want ErrProfileNotFound", err)
	}
	if err := m.Delete("id1"); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("二重削除のエラー = %v, want ErrProfileNotFound", err)
	}
}

func TestManager_ActiveProfileID(t *testing.T) {
	m := NewManagerAt(t.TempDir())
	p1 := Profile{ID: "id1", Name: "a", SpaceURL: "https://example.backlog.jp"}
	p2 := Profile{ID: "id2", Name: "b", SpaceURL: "https://example2.backlog.com"}
	if err := m.Upsert(p1); err != nil {
		t.Fatal(err)
	}
	if err := m.Upsert(p2); err != nil {
		t.Fatal(err)
	}

	// 初期状態は未選択
	id, err := m.GetActiveProfileID()
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Errorf("初期の activeProfileId = %q, want 空", id)
	}

	// 存在しない ID は拒否
	if err := m.SetActiveProfileID("nope"); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("存在しない ID の設定エラー = %v, want ErrProfileNotFound", err)
	}

	if err := m.SetActiveProfileID("id1"); err != nil {
		t.Fatal(err)
	}
	id, err = m.GetActiveProfileID()
	if err != nil {
		t.Fatal(err)
	}
	if id != "id1" {
		t.Errorf("activeProfileId = %q, want id1", id)
	}

	// 接続中プロファイルを削除すると選択が解除される
	if err := m.Delete("id1"); err != nil {
		t.Fatal(err)
	}
	id, err = m.GetActiveProfileID()
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Errorf("削除後の activeProfileId = %q, want 空", id)
	}

	// 空文字(選択解除)は許可
	if err := m.SetActiveProfileID(""); err != nil {
		t.Errorf("選択解除でエラー: %v", err)
	}
}

func TestManager_ConcurrentUpsertDoesNotLoseUpdates(t *testing.T) {
	m := NewManagerAt(t.TempDir())
	const n = 20
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			done <- m.Upsert(Profile{
				ID:       fmt.Sprintf("id%02d", i),
				Name:     "p",
				SpaceURL: "https://example.backlog.jp",
			})
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != n {
		t.Errorf("並行 Upsert 後のプロファイル数 = %d, want %d(更新消失)", len(list), n)
	}
}

func TestManager_FileDoesNotContainAPIKeyField(t *testing.T) {
	m := NewManagerAt(t.TempDir())
	if err := m.Upsert(Profile{ID: "id1", Name: "n", SpaceURL: "https://example.backlog.jp"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	// Profile 構造体に API キーのフィールドが存在しないことの回帰チェック
	if strings.Contains(strings.ToLower(string(data)), "apikey") {
		t.Error("config.json に apiKey らしきフィールドが含まれている")
	}
}
