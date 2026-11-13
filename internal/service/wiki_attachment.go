package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"sync"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/fileutil"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

type WikiAttachmentDownloadTicket struct {
	RunID string `json:"runId"`
	Code  string `json:"code"`
}
type WikiAttachmentDownloadResult struct {
	RunID       string `json:"runId"`
	State       string `json:"state"`
	Code        string `json:"code"`
	Bytes       int64  `json:"bytes"`
	SizeChanged bool   `json:"sizeChanged"`
}
type WikiAttachmentDownloadProgress struct {
	RunID         string `json:"runId"`
	ProjectID     int64  `json:"projectId"`
	WikiID        int64  `json:"wikiId"`
	AttachmentID  int64  `json:"attachmentId"`
	Phase         string `json:"phase"`
	ReceivedBytes int64  `json:"receivedBytes"`
	TotalBytes    int64  `json:"totalBytes"`
	Code          string `json:"code"`
}
type attachmentRun struct {
	id, profile               string
	project, wiki, attachment int64
	ctx                       context.Context
	cancel                    context.CancelFunc
	done                      chan struct{}
	started                   bool
	timer                     *time.Timer
}
type attachmentState struct {
	gate   sync.RWMutex
	mu     sync.Mutex
	run    *attachmentRun
	closed bool
}
type attachmentAPI interface {
	DownloadWikiAttachmentWithProgress(context.Context, int64, int64, io.Writer, func(int64, int64)) (backlogclient.AttachmentTransfer, error)
}

func validAttachmentID(id int64) bool { return id > 0 && id <= 9007199254740991 }
func (s *ProfileService) attachmentMetadata(ctx context.Context, profile string, project, wiki, attachment int64) (*store.WikiAttachment, string) {
	st, err := s.storeForProfile(profile)
	if err != nil {
		return nil, "local_data"
	}
	var result *store.WikiAttachment
	code := ""
	err = st.WithReadTx(ctx, func(tx *sql.Tx) error {
		raw, e := store.GetProjectRawJSON(ctx, tx, project)
		if errors.Is(e, sql.ErrNoRows) || (e == nil && raw == "") {
			code = "not_found"
			return nil
		}
		if e != nil {
			return e
		}
		disabled, e := store.WikiDisabled(ctx, tx, project)
		if e != nil {
			return e
		}
		if !syncpkg.WikiEnabled(raw) || disabled {
			code = "disabled"
			return nil
		}
		result, e = store.GetWikiAttachment(ctx, tx, project, wiki, attachment)
		if errors.Is(e, sql.ErrNoRows) {
			code = "not_found"
			return nil
		}
		return e
	})
	if err != nil {
		return nil, "local_data"
	}
	return result, code
}
func (s *ProfileService) BeginWikiAttachmentDownload(ctx context.Context, project, wiki, attachment int64) *WikiAttachmentDownloadTicket {
	result := &WikiAttachmentDownloadTicket{}
	if !validAttachmentID(project) || !validAttachmentID(wiki) || !validAttachmentID(attachment) {
		result.Code = "not_found"
		return result
	}
	s.attachments.gate.RLock()
	defer s.attachments.gate.RUnlock()
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	s.attachments.mu.Lock()
	defer s.attachments.mu.Unlock()
	if s.attachments.closed {
		result.Code = "cancelled"
		return result
	}
	if s.attachments.run != nil {
		result.Code = "busy"
		return result
	}
	if !s.syncMu.TryLock() {
		result.Code = "busy"
		return result
	}
	defer s.syncMu.Unlock()
	profile, err := s.GetActiveProfile()
	if err != nil || profile == "" {
		result.Code = "local_data"
		return result
	}
	if _, code := s.attachmentMetadata(ctx, profile, project, wiki, attachment); code != "" {
		result.Code = code
		return result
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := &attachmentRun{id: rand.Text(), profile: profile, project: project, wiki: wiki, attachment: attachment, ctx: runCtx, cancel: cancel, done: make(chan struct{})}
	s.attachments.run = run
	result.RunID = run.id
	run.timer = time.AfterFunc(30*time.Second, func() {
		s.attachments.mu.Lock()
		defer s.attachments.mu.Unlock()
		if s.attachments.run == run && !run.started {
			s.finishAttachmentLocked(run)
		}
	})
	return result
}
func (s *ProfileService) finishAttachmentLocked(run *attachmentRun) {
	run.cancel()
	if run.timer != nil {
		run.timer.Stop()
	}
	if s.attachments.run == run {
		s.attachments.run = nil
		close(run.done)
	}
}
func matchesAttachment(r *attachmentRun, p, w, a int64) bool {
	return r != nil && r.project == p && r.wiki == w && r.attachment == a
}
func (s *ProfileService) CancelWikiAttachmentDownload(project, wiki, attachment int64) {
	s.attachments.mu.Lock()
	r := s.attachments.run
	if matchesAttachment(r, project, wiki, attachment) {
		r.cancel()
		if !r.started {
			s.finishAttachmentLocked(r)
		}
	}
	s.attachments.mu.Unlock()
}

// transitionAttachment は新規予約を閉じ、稼働側の清掃後に接続設定を変更する。
func (s *ProfileService) transitionAttachment(closeService bool) func() {
	s.attachments.gate.Lock()
	s.attachments.mu.Lock()
	if closeService {
		s.attachments.closed = true
	}
	r := s.attachments.run
	if r != nil {
		r.cancel()
		if !r.started {
			s.finishAttachmentLocked(r)
		}
	}
	s.attachments.mu.Unlock()
	if r != nil {
		<-r.done
	}
	return s.attachments.gate.Unlock
}
func (s *ProfileService) AttachmentDownloadActive() bool {
	s.attachments.mu.Lock()
	defer s.attachments.mu.Unlock()
	return s.attachments.run != nil
}
func (s *ProfileService) CancelAllWikiAttachmentDownloads() {
	release := s.transitionAttachment(false)
	release()
}
func attachmentCode(err error) string {
	var disk *fileutil.Error
	var transfer *backlogclient.AttachmentError
	switch {
	case errors.As(err, &disk):
		return disk.Code
	case errors.As(err, &transfer):
		return transfer.Code
	case errors.Is(err, backlogclient.ErrRateLimitExceeded):
		return "rate_limited"
	default:
		return imageErrorCode(err)
	}
}

// ダイアログ注入により、保存先選択前の通信がないことを GUI なしで検査する。
func (s *ProfileService) DownloadWikiAttachment(ctx context.Context, project, wiki, attachment int64, dialog func(string) (string, error), progress func(WikiAttachmentDownloadProgress)) *WikiAttachmentDownloadResult {
	out := &WikiAttachmentDownloadResult{State: "cancelled", Code: "cancelled"}
	s.attachments.mu.Lock()
	r := s.attachments.run
	if !matchesAttachment(r, project, wiki, attachment) || r.started {
		s.attachments.mu.Unlock()
		return out
	}
	r.started = true
	r.timer.Stop()
	out.RunID = r.id
	s.attachments.mu.Unlock()
	defer func() { s.attachments.mu.Lock(); s.finishAttachmentLocked(r); s.attachments.mu.Unlock() }()
	emit := func(phase string, received, total int64, code string) {
		if progress != nil {
			progress(WikiAttachmentDownloadProgress{RunID: r.id, ProjectID: project, WikiID: wiki, AttachmentID: attachment, Phase: phase, ReceivedBytes: received, TotalBytes: total, Code: code})
		}
	}
	defer func() { emit(out.State, out.Bytes, -1, out.Code) }()
	fail := func(code string) *WikiAttachmentDownloadResult {
		out.Code = code
		out.State = "failed"
		if code == "cancelled" {
			out.State = "cancelled"
		}
		return out
	}
	if r.ctx.Err() != nil {
		return fail("cancelled")
	}
	s.profileMu.RLock()
	metadata, code := s.attachmentMetadata(r.ctx, r.profile, project, wiki, attachment)
	s.profileMu.RUnlock()
	if code != "" {
		return fail(code)
	}
	emit("selecting", 0, -1, "")
	path, err := dialog(fileutil.SafeAttachmentFilename(metadata.Name))
	if r.ctx.Err() != nil {
		return fail("cancelled")
	}
	if err != nil {
		return fail("dialog")
	}
	if path == "" {
		return fail("cancelled")
	}
	s.profileMu.RLock()
	defer s.profileMu.RUnlock()
	if !s.syncMu.TryLock() {
		return fail("busy")
	}
	defer s.syncMu.Unlock()
	metadata, code = s.attachmentMetadata(r.ctx, r.profile, project, wiki, attachment)
	if r.ctx.Err() != nil {
		return fail("cancelled")
	}
	if code != "" {
		return fail(code)
	}
	client, err := s.clientForProfile(r.ctx, r.profile)
	if err != nil {
		return fail(attachmentCode(err))
	}
	api, ok := client.(attachmentAPI)
	if !ok {
		return fail("network")
	}
	stop := context.AfterFunc(ctx, r.cancel)
	defer stop()
	ops := fileutil.DefaultAtomicOps()
	if s.attachmentFileOps != nil {
		ops = *s.attachmentFileOps
	}
	err = fileutil.WriteAtomicWithOps(r.ctx, path, ".wiki-attachment-*.tmp", func(w io.Writer) error {
		transfer, e := api.DownloadWikiAttachmentWithProgress(r.ctx, wiki, attachment, w, func(n, total int64) { emit("downloading", n, total, "") })
		out.Bytes = transfer.ReceivedBytes
		if e != nil {
			return e
		}
		if transfer.ContentLength >= 0 && transfer.ReceivedBytes != transfer.ContentLength {
			return &backlogclient.AttachmentError{Code: "truncated"}
		}
		if out.Bytes != metadata.Size {
			return &backlogclient.AttachmentError{Code: "size_mismatch"}
		}
		emit("saving", out.Bytes, transfer.ContentLength, "")
		return nil
	}, func(rename func() error) error {
		s.attachments.mu.Lock()
		defer s.attachments.mu.Unlock()
		return rename()
	}, ops)
	if err != nil {
		return fail(attachmentCode(err))
	}
	out.State = "saved"
	out.Code = ""
	return out
}

func (s *ProfileService) lockWikiOperation() bool {
	s.attachments.mu.Lock()
	defer s.attachments.mu.Unlock()
	if s.attachments.run != nil {
		return false
	}
	return s.syncMu.TryLock()
}
