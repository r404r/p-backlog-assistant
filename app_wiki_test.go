package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"backlog-assistant/internal/service"
)

func TestWikiBeforeCloseDiscardReentry(t *testing.T) {
	ctx := context.Background()
	a := NewApp()
	a.ctx = ctx
	a.SetWikiWindowState(true, false, "en")
	cleanupStarted, cleanupRelease, quit := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	a.wikiWindowHooks = &wikiWindowHooks{
		messageDialog: func(_ context.Context, options wailsruntime.MessageDialogOptions) (string, error) {
			calls++
			if calls > 1 {
				return "Cancel", nil
			}
			return options.Buttons[0], nil
		},
		attachmentActive:  func() bool { return true },
		cancelAttachments: func() { close(cleanupStarted); <-cleanupRelease },
		quit:              func(context.Context) { close(quit) },
	}
	if !a.beforeClose(ctx) {
		t.Fatal("終了前に後片付けを待つ必要がある")
	}
	<-cleanupStarted
	if a.wikiDirty {
		t.Error("破棄を選んだ変更が未保存のまま")
	}
	// 後片付け中に状態通知が再度届いても、確認を増やさず終了を待つ。
	a.SetWikiWindowState(true, true, "en")
	// 後片付け中の再入場でも確認を繰り返さない。
	blockedDuringCleanup := a.beforeClose(ctx)
	close(cleanupRelease)
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("後片付け後に終了しない")
	}
	if !blockedDuringCleanup {
		t.Error("後片付け前に閉じた")
	}
	if a.beforeClose(ctx) {
		t.Error("後片付け済みの再入場が拒否された")
	}
	if calls != 1 {
		t.Errorf("確認回数=%d, want 1", calls)
	}
}

func TestWikiBeforeCloseWithoutDraftWaitsForAttachment(t *testing.T) {
	a := NewApp()
	a.ctx = context.Background()
	started, release, quit := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseCleanup := sync.OnceFunc(func() { close(release) })
	defer releaseCleanup()
	a.wikiWindowHooks = &wikiWindowHooks{
		messageDialog: func(context.Context, wailsruntime.MessageDialogOptions) (string, error) {
			t.Error("未編集で確認を表示した")
			return "", nil
		},
		attachmentActive:  func() bool { return true },
		cancelAttachments: func() { close(started); <-release },
		quit:              func(context.Context) { close(quit) },
	}
	if !a.beforeClose(a.ctx) {
		t.Fatal("未編集でもダウンロードの後片付けを待つ必要がある")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("キャンセルが開始されない")
	}
	select {
	case <-quit:
		t.Fatal("後片付けより先に終了した")
	default:
	}
	releaseCleanup()
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("後片付け後に終了しない")
	}
}

func TestWikiAttachmentLogUsesOnlySafeMetadata(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	result := &service.WikiAttachmentDownloadResult{RunID: "https://example.invalid/?apiKey=FAKE-SECRET /outside-private/sample", State: "failed", Code: "disk", Bytes: 4}
	attrs := wikiAttachmentLogAttrs(1, 2, 3, result, time.Second)
	logger.LogAttrs(context.Background(), slog.LevelInfo, "Wiki 添付の保存", attrs...)
	for _, private := range []string{"example.invalid", "FAKE-SECRET", "outside-private"} {
		if strings.Contains(output.String(), private) {
			t.Fatal(output.String())
		}
	}
	if len(attrs) != 7 || !strings.Contains(output.String(), `"code":"disk"`) || !strings.Contains(output.String(), `"bytes":4`) {
		t.Fatal(output.String())
	}
}

func TestWikiBeforeCloseCancelThenRetry(t *testing.T) {
	for _, dialogError := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "dialog-error"}[dialogError], func(t *testing.T) {
			a := NewApp()
			ctx := context.Background()
			a.ctx = ctx
			a.SetWikiWindowState(true, false, "ja")
			cleanupStarted, release, quit := make(chan struct{}), make(chan struct{}), make(chan struct{})
			calls := 0
			a.wikiWindowHooks = &wikiWindowHooks{
				messageDialog: func(_ context.Context, o wailsruntime.MessageDialogOptions) (string, error) {
					calls++
					if calls == 1 {
						if dialogError {
							return "", errors.New("dialog unavailable")
						}
						return o.CancelButton, nil
					}
					return o.Buttons[0], nil
				},
				attachmentActive:  func() bool { return true },
				cancelAttachments: func() { close(cleanupStarted); <-release },
				quit:              func(context.Context) { close(quit) },
			}
			if !a.beforeClose(ctx) || !a.wikiDirty || a.wikiClosing || a.wikiCloseReady {
				t.Fatal("キャンセル後の状態が変わった")
			}
			select {
			case <-cleanupStarted:
				t.Fatal("確認拒否でダウンロードを取り消した")
			default:
			}
			if !a.beforeClose(ctx) {
				t.Fatal("次の終了が後片付けを待たない")
			}
			<-cleanupStarted
			close(release)
			select {
			case <-quit:
			case <-time.After(time.Second):
				t.Fatal("終了要求がない")
			}
			if a.beforeClose(ctx) || calls != 2 {
				t.Fatalf("再入場で再確認された: %d", calls)
			}
		})
	}
}

func TestWikiAttachmentSaveDialogAllowsAnyFile(t *testing.T) {
	for _, language := range []string{"ja", "en"} {
		a := NewApp()
		a.ctx = context.Background()
		a.SetWikiWindowState(false, false, language)
		a.wikiWindowHooks = &wikiWindowHooks{saveDialog: func(_ context.Context, options wailsruntime.SaveDialogOptions) (string, error) {
			if len(options.Filters) != 0 {
				t.Errorf("全ファイルの保存に拡張子フィルターがある: %+v", options.Filters)
			}
			if options.DefaultFilename != "架空.txt" || options.DefaultDirectory != "" {
				t.Errorf("既定値=%+v", options)
			}
			if language == "en" && options.Title != "Save attachment" {
				t.Error(options.Title)
			}
			return "", nil
		}}
		if path, err := a.saveWikiAttachment("架空.txt"); path != "" || err != nil {
			t.Fatal(path, err)
		}
	}
}
