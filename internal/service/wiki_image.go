package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"regexp"
	"sync"
	"time"

	"backlog-assistant/internal/backlogclient"
	"backlog-assistant/internal/store"
)

const wikiImageLimit = 8 << 20

type WikiImageInput struct {
	ProjectID       int64  `json:"projectId"`
	WikiID          int64  `json:"wikiId"`
	AttachmentID    int64  `json:"attachmentId"`
	ExpectedUpdated string `json:"expectedUpdated"`
	RequestID       string `json:"requestId"`
}
type WikiImageResult struct {
	Code       string `json:"code"`
	MIME       string `json:"mime"`
	DataBase64 string `json:"dataBase64"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}
type wikiImageRun struct {
	ctx     context.Context
	cancel  context.CancelFunc
	expires time.Time
	input   WikiImageInput
	once    sync.Once
	detail  *backlogclient.Wiki
	err     error
}
type wikiImageAPI interface {
	GetWiki(context.Context, int64) (*backlogclient.Wiki, error)
	DownloadWikiAttachment(context.Context, int64, int64, io.Writer) error
}

var imageRequestID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
var errImageLimit = errors.New("画像の上限を超えました")

type imageBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *imageBuffer) Write(p []byte) (int, error) {
	if len(p) > wikiImageLimit-b.Len() {
		b.exceeded = true
		return 0, errImageLimit
	}
	return b.Buffer.Write(p)
}
func imageFailure(code string) *WikiImageResult { return &WikiImageResult{Code: code} }
func normalizeWikiImage(data []byte) *WikiImageResult {
	if len(data) > wikiImageLimit {
		return imageFailure("too_large")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return imageFailure("unsupported_type")
	}
	if format != "png" && format != "jpeg" && format != "gif" {
		return imageFailure("unsupported_type")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 8192 || cfg.Height > 8192 || cfg.Width > 16000000/cfg.Height {
		return imageFailure("too_large")
	}
	img, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil || decodedFormat != format || img.Bounds().Dx() != cfg.Width || img.Bounds().Dy() != cfg.Height {
		return imageFailure("invalid_image")
	}
	var out imageBuffer
	mime := "image/png"
	if format == "jpeg" {
		mime = "image/jpeg"
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(&out, img)
	}
	if out.exceeded {
		return imageFailure("too_large")
	}
	if err != nil {
		return imageFailure("invalid_image")
	}
	return &WikiImageResult{MIME: mime, DataBase64: base64.StdEncoding.EncodeToString(out.Bytes()), Width: cfg.Width, Height: cfg.Height}
}
func imageErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, backlogclient.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, backlogclient.ErrPermissionDenied):
		return "forbidden"
	case errors.Is(err, backlogclient.ErrNotFound):
		return "not_found"
	default:
		return "network"
	}
}

// imageRun は短いlockだけで世代を登録する。network待機中にprofile lockを保持しない。
func (s *ProfileService) imageRun(ctx context.Context, profile string, in WikiImageInput) (*wikiImageRun, chan struct{}, string) {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	if s.imageRuns == nil {
		s.imageRuns = map[string]*wikiImageRun{}
	}
	if s.imageSlots == nil {
		s.imageSlots = make(chan struct{}, 2)
	}
	now := time.Now()
	count := 0
	for key, run := range s.imageRuns {
		if !now.Before(run.expires) {
			run.cancel()
			delete(s.imageRuns, key)
		} else if len(key) > len(profile) && key[:len(profile)+1] == profile+"\x00" {
			count++
		}
	}
	key := wikiRunKey(profile, in.RequestID)
	if run := s.imageRuns[key]; run != nil {
		if run.ctx.Err() != nil {
			return nil, nil, "cancelled"
		}
		if run.input.ProjectID != in.ProjectID || run.input.WikiID != in.WikiID || run.input.ExpectedUpdated != in.ExpectedUpdated {
			return nil, nil, "stale"
		}
		return run, s.imageSlots, ""
	}
	if count >= 8 {
		var oldestKey string
		var oldest time.Time
		for k, r := range s.imageRuns {
			if len(k) > len(profile) && k[:len(profile)+1] == profile+"\x00" && r.ctx.Err() != nil && (oldestKey == "" || r.expires.Before(oldest)) {
				oldestKey = k
				oldest = r.expires
			}
		}
		if oldestKey == "" {
			return nil, nil, "busy"
		}
		delete(s.imageRuns, oldestKey)
	}
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	run := &wikiImageRun{ctx: runCtx, cancel: cancel, expires: now.Add(60 * time.Second), input: in}
	s.imageRuns[key] = run
	return run, s.imageSlots, ""
}
func (s *ProfileService) CancelWikiImageLoads(profile, requestID string) {
	if !imageRequestID.MatchString(requestID) {
		return
	}
	if _, err := s.cfg.Get(profile); err != nil {
		return
	}
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	key := wikiRunKey(profile, requestID)
	if run := s.imageRuns[key]; run != nil {
		run.cancel()
		return
	}
	// cancelが取得より先に届いても、短寿命の墓標でその世代を拒否する。
	if s.imageRuns == nil {
		s.imageRuns = map[string]*wikiImageRun{}
	}
	now := time.Now()
	count := 0
	for k, r := range s.imageRuns {
		if !now.Before(r.expires) {
			r.cancel()
			delete(s.imageRuns, k)
		} else if len(k) > len(profile) && k[:len(profile)+1] == profile+"\x00" {
			count++
		}
	}
	if count >= 8 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.imageRuns[key] = &wikiImageRun{ctx: ctx, cancel: cancel, expires: now.Add(60 * time.Second)}
}
func (s *ProfileService) cancelProfileImages(profile string) {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	for key, run := range s.imageRuns {
		if profile == "" || (len(key) > len(profile) && key[:len(profile)+1] == profile+"\x00") {
			run.cancel()
		}
	}
}
func (s *ProfileService) GetWikiAttachmentImage(ctx context.Context, profile string, in WikiImageInput) (*WikiImageResult, error) {
	runParent := ctx
	ctx, requestCancel := context.WithTimeout(ctx, 30*time.Second)
	defer requestCancel()
	if ctx.Err() != nil {
		return imageFailure(imageErrorCode(ctx.Err())), nil
	}
	if !imageRequestID.MatchString(in.RequestID) || in.ProjectID <= 0 || in.WikiID <= 0 || in.AttachmentID <= 0 {
		return imageFailure("not_found"), nil
	}
	if in.ExpectedUpdated == "" {
		return imageFailure("stale"), nil
	}
	// ローカルの所属と記法を通信前に確認する。
	s.profileMu.RLock()
	if !s.syncMu.TryLock() {
		s.profileMu.RUnlock()
		return imageFailure("busy"), nil
	}
	s.syncMu.Unlock()
	list, err := s.wikiListLocked(ctx, profile, store.WikiFilter{ProjectID: in.ProjectID})
	var page *WikiPage
	if err == nil && !list.Disabled {
		for i := range list.Pages {
			if list.Pages[i].ID == in.WikiID {
				page = &list.Pages[i]
				break
			}
		}
	}
	code := ""
	if page == nil {
		code = "not_found"
	} else if page.TextFormattingRule != "markdown" || page.ContentFetchedAt == "" || page.ContentUpdated != in.ExpectedUpdated {
		code = "stale"
	} else {
		code = "not_own_attachment"
		for _, a := range page.Attachments {
			if a.ID == in.AttachmentID && a.WikiID == in.WikiID {
				code = ""
				if a.Size > wikiImageLimit {
					code = "too_large"
				}
				break
			}
		}
	}
	var api wikiImageAPI
	var run *wikiImageRun
	var slots chan struct{}
	if code == "" {
		run, slots, code = s.imageRun(runParent, profile, in)
		if code == "" {
			var c connector
			c, err = s.clientForProfile(ctx, profile)
			if err != nil {
				code = imageErrorCode(err)
			} else {
				api, _ = c.(wikiImageAPI)
				if api == nil {
					code = "network"
				}
			}
		}
	}
	s.profileMu.RUnlock()
	if ctx.Err() != nil {
		return imageFailure(imageErrorCode(ctx.Err())), nil
	}
	if code != "" {
		return imageFailure(code), nil
	}
	slotCtx, cancel := context.WithTimeout(run.ctx, 30*time.Second)
	defer cancel()
	// Appの呼出しcontextも個別に反映する。
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-slotCtx.Done():
		return imageFailure(imageErrorCode(slotCtx.Err())), nil
	}
	run.once.Do(func() { run.detail, run.err = api.GetWiki(slotCtx, in.WikiID) })
	if slotCtx.Err() != nil {
		return imageFailure(imageErrorCode(slotCtx.Err())), nil
	}
	if run.err != nil {
		return imageFailure(imageErrorCode(run.err)), nil
	}
	remote := run.detail
	if remote == nil || remote.ID != in.WikiID || remote.ProjectID != in.ProjectID {
		return imageFailure("not_own_attachment"), nil
	}
	if remote.Updated != in.ExpectedUpdated {
		return imageFailure("stale"), nil
	}
	found := false
	for _, a := range remote.Attachments {
		if a.ID == in.AttachmentID {
			found = true
			if a.Size > wikiImageLimit {
				return imageFailure("too_large"), nil
			}
			break
		}
	}
	if !found {
		return imageFailure("not_own_attachment"), nil
	}
	var b imageBuffer
	err = api.DownloadWikiAttachment(slotCtx, in.WikiID, in.AttachmentID, &b)
	if b.exceeded {
		return imageFailure("too_large"), nil
	}
	if slotCtx.Err() != nil {
		return imageFailure(imageErrorCode(slotCtx.Err())), nil
	}
	if err != nil {
		return imageFailure(imageErrorCode(err)), nil
	}
	result := normalizeWikiImage(b.Bytes())
	if slotCtx.Err() != nil {
		return imageFailure(imageErrorCode(slotCtx.Err())), nil
	}
	return result, nil
}
