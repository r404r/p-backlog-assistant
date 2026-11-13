package service

import (
	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/fileutil"
	"backlog-assistant/internal/store"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type attachmentConnector struct {
	*fakeConnector
	calls   int
	failure error
	started chan struct{}
}

func (c *attachmentConnector) DownloadWikiAttachmentWithProgress(ctx context.Context, _, _ int64, w io.Writer, progress func(int64, int64)) (backlogclient.AttachmentTransfer, error) {
	c.calls++
	if c.started != nil {
		close(c.started)
		<-ctx.Done()
		return backlogclient.AttachmentTransfer{}, ctx.Err()
	}
	if c.failure != nil {
		return backlogclient.AttachmentTransfer{}, c.failure
	}
	progress(0, 4)
	n, err := io.WriteString(w, "data")
	return backlogclient.AttachmentTransfer{ReceivedBytes: int64(n), ContentLength: 4}, err
}

type failingAttachmentFile struct {
	fileutil.File
	stage   string
	failure error
}

func (f failingAttachmentFile) Write(p []byte) (int, error) {
	if f.stage == "write" {
		return 0, f.failure
	}
	return f.File.Write(p)
}
func (f failingAttachmentFile) Close() error {
	e := f.File.Close()
	if f.stage == "close" {
		return f.failure
	}
	return e
}
func TestAttachmentFailuresNeverExposeSourceErrors(t *testing.T) {
	for _, stage := range []string{"read", "write", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			s, c, _ := attachmentService(t)
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			ctx := context.Background()
			failure := errors.New("https://example.invalid/file?apiKey=FAKE-SECRET /outside-private/sample")
			ops := fileutil.DefaultAtomicOps()
			create := ops.CreateTemp
			ops.CreateTemp = func(d, p string) (fileutil.File, error) {
				f, e := create(d, p)
				return failingAttachmentFile{f, stage, failure}, e
			}
			if stage == "rename" {
				ops.Rename = func(string, string) error { return failure }
			}
			if stage == "read" {
				c.failure = failure
			}
			s.attachmentFileOps = &ops
			s.BeginWikiAttachmentDownload(ctx, 1, 2, 3)
			var events []WikiAttachmentDownloadProgress
			result := s.DownloadWikiAttachment(ctx, 1, 2, 3, func(string) (string, error) { return filepath.Join(t.TempDir(), "sample.txt"), nil }, func(p WikiAttachmentDownloadProgress) { events = append(events, p) })
			data, _ := json.Marshal([]any{result, events})
			text := string(data) + logs.String()
			if result.State != "failed" {
				t.Fatal(result)
			}
			for _, private := range []string{"FAKE-SECRET", "example.invalid", "outside-private"} {
				if strings.Contains(text, private) {
					t.Fatal(text)
				}
			}
		})
	}
}

func TestAttachmentCancelAndProfileChangeWaitForCleanup(t *testing.T) {
	for _, transition := range []string{"cancel", "profile", "close"} {
		t.Run(transition, func(t *testing.T) {
			s, c, _ := attachmentService(t)
			c.started = make(chan struct{})
			ctx := context.Background()
			dir := t.TempDir()
			target := filepath.Join(dir, "sample.txt")
			s.BeginWikiAttachmentDownload(ctx, 1, 2, 3)
			result := make(chan *WikiAttachmentDownloadResult, 1)
			go func() {
				result <- s.DownloadWikiAttachment(ctx, 1, 2, 3, func(string) (string, error) { return target, nil }, nil)
			}()
			<-c.started
			if _, err := s.SyncWikis(ctx, cfgActive(t, s), 1, "run"); err == nil {
				t.Fatal("sync accepted during download")
			}
			switch transition {
			case "cancel":
				s.CancelWikiAttachmentDownload(1, 2, 3)
			case "profile":
				if err := s.SetActiveProfile(""); err != nil {
					t.Fatal(err)
				}
			case "close":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case out := <-result:
				if out.State != "cancelled" {
					t.Fatal(out)
				}
			case <-time.After(time.Second):
				t.Fatal("cleanup did not finish")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 || s.AttachmentDownloadActive() {
				t.Fatal(entries)
			}
		})
	}
}
func cfgActive(t *testing.T, s *ProfileService) string {
	t.Helper()
	id, err := s.GetActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func attachmentService(t *testing.T) (*ProfileService, *attachmentConnector, *store.Store) {
	t.Helper()
	ctx := context.Background()
	c := &attachmentConnector{fakeConnector: &fakeConnector{info: testInfo()}}
	s, _, _ := newTestService(t, c.fakeConnector)
	s.newClient = func(string, string) (connector, error) { return c, nil }
	saved, err := s.SaveProfile(ctx, "", "架空", "https://example.backlog.jp", "DUMMY-KEY")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetActiveProfile(saved.Profile.ID); err != nil {
		t.Fatal(err)
	}
	st, err := s.storeForProfile(saved.Profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.UpsertProject(ctx, &store.Project{ID: 1, ProjectKey: "SAMPLE", RawJSON: `{"useWiki":true}`}); err != nil {
		t.Fatal(err)
	}
	err = st.WithTx(ctx, func(tx *sql.Tx) error {
		if e := store.UpsertWikiList(ctx, tx, &store.Wiki{ID: 2, ProjectID: 1, Name: "架空", Updated: "T1", TagsJSON: "[]", RawJSON: "{}"}, "now"); e != nil {
			return e
		}
		return store.SaveWikiContent(ctx, tx, 2, "sample", "T1", "now", []store.WikiAttachment{{ID: 3, Name: "CON.txt", Size: 4}})
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, c, st
}
func TestAttachmentOwnershipDialogAndOriginalBytes(t *testing.T) {
	s, c, _ := attachmentService(t)
	ctx := context.Background()
	if ticket := s.BeginWikiAttachmentDownload(ctx, 9, 2, 3); ticket.Code != "not_found" {
		t.Fatal(ticket)
	}
	ticket := s.BeginWikiAttachmentDownload(ctx, 1, 2, 3)
	if ticket.Code != "" || ticket.RunID == "" {
		t.Fatal(ticket)
	}
	if got := s.BeginWikiAttachmentDownload(ctx, 1, 2, 3); got.Code != "busy" {
		t.Fatal(got)
	}
	result := s.DownloadWikiAttachment(ctx, 1, 2, 3, func(name string) (string, error) {
		if name != "CON_.txt" {
			t.Fatal(name)
		}
		return "", nil
	}, nil)
	if result.State != "cancelled" || c.calls != 0 {
		t.Fatal(result, c.calls)
	}
	target := filepath.Join(t.TempDir(), "sample.txt")
	s.BeginWikiAttachmentDownload(ctx, 1, 2, 3)
	result = s.DownloadWikiAttachment(ctx, 1, 2, 3, func(string) (string, error) { return target, nil }, nil)
	data, _ := os.ReadFile(target)
	if result.State != "saved" || string(data) != "data" || c.calls != 1 {
		t.Fatal(result, string(data), c.calls)
	}
}

func TestAttachmentMissingReservationIsCancelled(t *testing.T) {
	s, c, _ := attachmentService(t)
	ctx := context.Background()
	dialogs := 0
	dialog := func(string) (string, error) { dialogs++; return "", nil }
	for _, scenario := range []string{"missing", "cancelled", "expired", "started"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario != "missing" {
				s.BeginWikiAttachmentDownload(ctx, 1, 2, 3)
				switch scenario {
				case "cancelled":
					s.CancelWikiAttachmentDownload(1, 2, 3)
				case "expired":
					s.attachments.mu.Lock()
					s.finishAttachmentLocked(s.attachments.run)
					s.attachments.mu.Unlock()
				case "started":
					s.attachments.mu.Lock()
					s.attachments.run.started = true
					s.attachments.mu.Unlock()
					t.Cleanup(func() {
						s.attachments.mu.Lock()
						s.finishAttachmentLocked(s.attachments.run)
						s.attachments.mu.Unlock()
					})
				}
			}
			out := s.DownloadWikiAttachment(ctx, 1, 2, 3, dialog, nil)
			if out.State != "cancelled" || out.Code != "cancelled" || dialogs != 0 || c.calls != 0 {
				t.Fatalf("%+v dialogs=%d http=%d", out, dialogs, c.calls)
			}
		})
	}
}
func TestAttachmentSecondOwnershipCheckAndMismatch(t *testing.T) {
	s, c, st := attachmentService(t)
	ctx := context.Background()
	target := filepath.Join(t.TempDir(), "sample.txt")
	s.BeginWikiAttachmentDownload(ctx, 1, 2, 3)
	result := s.DownloadWikiAttachment(ctx, 1, 2, 3, func(string) (string, error) {
		_, err := st.DB().Exec(`UPDATE wiki_attachments SET size=5 WHERE id=3`)
		return target, err
	}, nil)
	if result.Code != "size_mismatch" || c.calls != 1 {
		t.Fatal(result, c.calls)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s.BeginWikiAttachmentDownload(ctx, 1, 2, 3)
	result = s.DownloadWikiAttachment(ctx, 1, 2, 3, func(string) (string, error) {
		_, err := st.DB().Exec(`UPDATE wikis SET deleted=1 WHERE id=2`)
		return target, err
	}, nil)
	if result.Code != "not_found" || c.calls != 1 {
		t.Fatal(result, c.calls)
	}
}

func TestAttachmentCleanupFailurePreservesTarget(t *testing.T) {
	s, _, _ := attachmentService(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "架空.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := fileutil.DefaultAtomicOps()
	ops.Rename = func(string, string) error { return os.ErrPermission }
	ops.Remove = func(string) error { return errors.New("FAKE-SECRET /outside-private/sample") }
	s.attachmentFileOps = &ops
	ctx := context.Background()
	s.BeginWikiAttachmentDownload(ctx, 1, 2, 3)
	var final WikiAttachmentDownloadProgress
	result := s.DownloadWikiAttachment(ctx, 1, 2, 3, func(string) (string, error) { return target, nil }, func(p WikiAttachmentDownloadProgress) { final = p })
	if result.State != "failed" || result.Code != "cleanup_failed" || final.Code != "cleanup_failed" || final.Phase != "failed" {
		t.Fatalf("後片付け失敗が通知されない: %+v %+v", result, final)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "old" || s.AttachmentDownloadActive() {
		t.Fatalf("既存ファイルか予約が変わった: %q %v", data, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 2 {
		t.Fatalf("削除失敗した一時ファイルを確認できない: %v %v", files, err)
	}
}

func TestWikiOperationWaitsForOrdinaryOperation(t *testing.T) {
	s, _, _ := attachmentService(t)
	s.syncMu.Lock()
	locked := make(chan bool, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		ok := s.lockWikiOperation()
		if ok {
			s.syncMu.Unlock()
		}
		locked <- ok
	}()
	<-started
	select {
	case ok := <-locked:
		s.syncMu.Unlock()
		t.Fatalf("通常処理を待たず戻った: %v", ok)
	case <-time.After(30 * time.Millisecond):
	}
	s.syncMu.Unlock()
	select {
	case ok := <-locked:
		if !ok {
			t.Fatal("先行処理の完了後も取得できない")
		}
	case <-time.After(time.Second):
		t.Fatal("通常処理の完了後に再開しない")
	}
}
