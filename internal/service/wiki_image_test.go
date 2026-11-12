package service

import (
	"backlog-assistant/internal/backlogclient"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"backlog-assistant/internal/store"
)

func TestNormalizeWikiImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	for _, format := range []string{"png", "jpeg", "gif"} {
		t.Run(format, func(t *testing.T) {
			var b bytes.Buffer
			switch format {
			case "png":
				_ = png.Encode(&b, img)
			case "jpeg":
				_ = jpeg.Encode(&b, img, nil)
			case "gif":
				_ = gif.Encode(&b, img, nil)
			}
			r := normalizeWikiImage(b.Bytes())
			if r.Code != "" || r.Width != 4 || r.Height != 3 || r.DataBase64 == "" {
				t.Fatalf("unexpected result: code=%s %dx%d", r.Code, r.Width, r.Height)
			}
			if format == "gif" && r.MIME != "image/png" {
				t.Fatal("GIF must become a still PNG")
			}
		})
	}
	for _, data := range [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), []byte("<html>secret</html>"), {137, 80, 78, 71, 13, 10, 26, 10}, make([]byte, (8<<20)+1)} {
		r := normalizeWikiImage(data)
		if r.Code == "" || r.DataBase64 != "" || r.MIME != "" {
			t.Fatal("invalid image leaked data")
		}
	}
}

type imageConnector struct {
	*wikiWriter
	data        []byte
	downloadErr error
	calls       atomic.Int32
	active      atomic.Int32
	peak        atomic.Int32
	entered     chan struct{}
	release     chan struct{}
	stream      func(io.Writer) error
}

func (c *imageConnector) DownloadWikiAttachment(ctx context.Context, wikiID, attachmentID int64, dst io.Writer) error {
	c.calls.Add(1)
	active := c.active.Add(1)
	defer c.active.Add(-1)
	for {
		old := c.peak.Load()
		if active <= old || c.peak.CompareAndSwap(old, active) {
			break
		}
	}
	if c.entered != nil {
		c.entered <- struct{}{}
	}
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if c.downloadErr != nil {
		return c.downloadErr
	}
	if c.stream != nil {
		return c.stream(dst)
	}
	_, err := dst.Write(c.data)
	return err
}
func wikiImageSetup(t *testing.T) (*ProfileService, string, *imageConnector, WikiImageInput) {
	t.Helper()
	s, id, f := wikiWriteSetup(t)
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 3)))
	f.remote.Attachments = []backlogclient.WikiAttachment{{ID: 7, Name: "image.png", Size: int64(b.Len())}, {ID: 8, Name: "second.png", Size: int64(b.Len())}}
	c := &imageConnector{wikiWriter: f, data: b.Bytes()}
	s.newClient = func(string, string) (connector, error) { return c, nil }
	if _, err := s.BeginWikiEdit(context.Background(), id, 1, 10); err != nil {
		t.Fatal(err)
	}
	return s, id, c, WikiImageInput{ProjectID: 1, WikiID: 10, AttachmentID: 7, ExpectedUpdated: "T1", RequestID: "img-test"}
}
func TestWikiImageAuthorizationAndFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mutate     func(*ProfileService, string, *imageConnector, *WikiImageInput)
	}{
		{"success", "", func(*ProfileService, string, *imageConnector, *WikiImageInput) {}},
		{"wrong project", "not_found", func(_ *ProfileService, _ string, _ *imageConnector, in *WikiImageInput) { in.ProjectID = 2 }},
		{"wrong wiki", "not_found", func(_ *ProfileService, _ string, _ *imageConnector, in *WikiImageInput) { in.WikiID = 11 }},
		{"wrong attachment", "not_own_attachment", func(_ *ProfileService, _ string, _ *imageConnector, in *WikiImageInput) { in.AttachmentID = 9 }},
		{"empty revision", "stale", func(_ *ProfileService, _ string, _ *imageConnector, in *WikiImageInput) { in.ExpectedUpdated = "" }},
		{"local revision", "stale", func(_ *ProfileService, _ string, _ *imageConnector, in *WikiImageInput) { in.ExpectedUpdated = "T2" }},
		{"remote revision", "stale", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) { c.remote.Updated = "T2" }},
		{"remote project", "not_own_attachment", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) { c.remote.ProjectID = 2 }},
		{"remote attachment", "not_own_attachment", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) { c.remote.Attachments = nil }},
		{"remote size", "too_large", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) {
			c.remote.Attachments[0].Size = (8 << 20) + 1
		}},
		{"stream limit", "too_large", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) {
			c.data = make([]byte, (8<<20)+1)
		}},
		{"forged format", "unsupported_type", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) {
			c.data = []byte("<svg><script>secret</script></svg>")
		}},
		{"unauthorized", "unauthorized", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) {
			c.getErr = backlogclient.ErrUnauthorized
		}},
		{"forbidden", "forbidden", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) {
			c.downloadErr = backlogclient.ErrPermissionDenied
		}},
		{"missing", "not_found", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) {
			c.downloadErr = backlogclient.ErrNotFound
		}},
		{"raw error hidden", "network", func(_ *ProfileService, _ string, c *imageConnector, _ *WikiImageInput) {
			c.downloadErr = errors.New("https://secret.invalid?apiKey=secret")
		}},
		{"early cancellation", "cancelled", func(s *ProfileService, id string, _ *imageConnector, in *WikiImageInput) {
			s.CancelWikiImageLoads(id, in.RequestID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, id, c, in := wikiImageSetup(t)
			before := c.gets
			tc.mutate(s, id, c, &in)
			r, err := s.GetWikiAttachmentImage(context.Background(), id, in)
			if err != nil || r.Code != tc.code {
				t.Fatalf("code=%s want=%s err=%v", r.Code, tc.code, err)
			}
			if tc.code != "" && (r.DataBase64 != "" || r.MIME != "" || r.Width != 0) {
				t.Fatal("failure returned image data")
			}
			if tc.code == "not_found" && in.ProjectID == 2 && (c.gets != before || c.calls.Load() != 0) {
				t.Fatal("local rejection made a request")
			}
		})
	}
}
func TestWikiImageSharedDetailBusyAndGeneration(t *testing.T) {
	s, id, c, in := wikiImageSetup(t)
	ctx := context.Background()
	before := c.gets
	for _, a := range []int64{7, 8} {
		in.AttachmentID = a
		r, err := s.GetWikiAttachmentImage(ctx, id, in)
		if err != nil || r.Code != "" {
			t.Fatal(r, err)
		}
	}
	if c.gets != before+1 {
		t.Fatal("detail was not shared")
	}
	in.WikiID = 12
	if r, _ := s.GetWikiAttachmentImage(ctx, id, in); r.Code != "not_found" {
		t.Fatal(r.Code)
	}
	in.WikiID = 10
	in.RequestID = "busy"
	s.syncMu.Lock()
	r, _ := s.GetWikiAttachmentImage(ctx, id, in)
	s.syncMu.Unlock()
	if r.Code != "busy" {
		t.Fatal(r.Code)
	}
	s.CancelWikiImageLoads(id, "before-load")
	in.RequestID = "before-load"
	if r, _ := s.GetWikiAttachmentImage(ctx, id, in); r.Code != "cancelled" {
		t.Fatal(r.Code)
	}
	in.RequestID = "next-page"
	if r, _ := s.GetWikiAttachmentImage(ctx, id, in); r.Code != "" {
		t.Fatal(r.Code)
	}
}
func TestWikiImageConcurrencyCancellationAndProfileSwitch(t *testing.T) {
	s, id, c, in := wikiImageSetup(t)
	c.entered = make(chan struct{}, 3)
	c.release = make(chan struct{})
	out := make(chan *WikiImageResult, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			input := in
			input.AttachmentID = 7 + int64(i%2)
			r, _ := s.GetWikiAttachmentImage(context.Background(), id, input)
			out <- r
		}(i)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-c.entered:
		case <-time.After(time.Second):
			t.Fatal("download did not start")
		}
	}
	s.CancelWikiImageLoads(id, in.RequestID)
	for i := 0; i < 3; i++ {
		select {
		case r := <-out:
			if r.Code != "cancelled" {
				t.Fatal(r.Code)
			}
		case <-time.After(time.Second):
			t.Fatal("cancel failed")
		}
	}
	if c.peak.Load() > 2 {
		t.Fatal("more than two streams")
	}
	close(c.release)
	in.RequestID = "after-cancel"
	if r, _ := s.GetWikiAttachmentImage(context.Background(), id, in); r.Code != "" {
		t.Fatal(r.Code)
	}
	if err := s.SetActiveProfile(id); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetWikiAttachmentImage(context.Background(), id, in); r.Code != "cancelled" {
		t.Fatal("old generation survived profile switch", r.Code)
	}
}
func TestWikiImageDecodeDimensionsAndStillFrame(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 8193, 1)))
	if r := normalizeWikiImage(b.Bytes()); r.Code != "too_large" {
		t.Fatal(r.Code)
	}
	b.Reset()
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 4001, 4000)))
	if r := normalizeWikiImage(b.Bytes()); r.Code != "too_large" {
		t.Fatal(r.Code)
	}
	b.Reset()
	pal := color.Palette{color.Black, color.White}
	frames := []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 2, 2), pal), image.NewPaletted(image.Rect(0, 0, 2, 2), pal)}
	frames[1].SetColorIndex(0, 0, 1)
	_ = gif.EncodeAll(&b, &gif.GIF{Image: frames, Delay: []int{1, 1}})
	r := normalizeWikiImage(b.Bytes())
	raw, _ := base64.StdEncoding.DecodeString(r.DataBase64)
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil || format != "png" || color.GrayModel.Convert(img.At(0, 0)) != (color.Gray{Y: 0}) {
		t.Fatal("first frame was not preserved", format, err)
	}
}

func TestWikiImageGenerationRegistryIsBoundedAndExpires(t *testing.T) {
	s, id, _, in := wikiImageSetup(t)
	for i := 0; i < 30; i++ {
		in.RequestID = fmt.Sprintf("page-%d", i)
		r, err := s.GetWikiAttachmentImage(context.Background(), id, in)
		if err != nil || r.Code != "" {
			t.Fatal(r.Code, err)
		}
		s.CancelWikiImageLoads(id, in.RequestID)
	}
	if len(s.imageRuns) > 8 {
		t.Fatal("generation registry exceeded limit", len(s.imageRuns))
	}
	for _, run := range s.imageRuns {
		run.expires = time.Now().Add(-time.Second)
	}
	in.RequestID = "after-expiry"
	if r, _ := s.GetWikiAttachmentImage(context.Background(), id, in); r.Code != "" {
		t.Fatal(r.Code)
	}
	if len(s.imageRuns) != 1 {
		t.Fatal("expired generations retained", len(s.imageRuns))
	}
}

func TestWikiImageCancelledCallerDoesNotReadNetwork(t *testing.T) {
	s, id, c, in := wikiImageSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := s.GetWikiAttachmentImage(ctx, id, in)
	if err != nil || r.Code != "cancelled" || c.calls.Load() != 0 {
		t.Fatal(r.Code, err)
	}
}

func TestWikiImageConcurrentRequestsWithoutSync(t *testing.T) {
	s, id, _, in := wikiImageSetup(t)
	for round := 0; round < 50; round++ {
		start := make(chan struct{})
		out := make(chan *WikiImageResult, 2)
		for _, attachment := range []int64{7, 8} {
			go func(attachment int64) {
				<-start
				input := in
				input.AttachmentID = attachment
				r, _ := s.GetWikiAttachmentImage(context.Background(), id, input)
				out <- r
			}(attachment)
		}
		close(start)
		for range 2 {
			r := <-out
			if r == nil || r.Code != "" {
				t.Errorf("round %d: unexpected result %+v without sync", round, r)
			}
		}
	}
	s.CancelWikiImageLoads(id, in.RequestID)
}

func TestWikiImageStreamStopsAtLimit(t *testing.T) {
	s, id, c, in := wikiImageSetup(t)
	const chunkSize = 64 << 10
	written, attempted := 0, 0
	var writeErr error
	c.stream = func(dst io.Writer) error {
		chunk := make([]byte, chunkSize)
		for attempted < 64<<20 {
			attempted += len(chunk)
			n, err := dst.Write(chunk)
			written += n
			if err != nil {
				writeErr = err
				return err
			}
		}
		return nil
	}
	r, err := s.GetWikiAttachmentImage(context.Background(), id, in)
	if err != nil || r.Code != "too_large" || !errors.Is(writeErr, errImageLimit) {
		t.Fatalf("code=%s err=%v writeErr=%v", r.Code, err, writeErr)
	}
	if written != wikiImageLimit || attempted != wikiImageLimit+chunkSize {
		t.Fatalf("stream did not stop at limit: written=%d attempted=%d", written, attempted)
	}
}

func TestWikiImageLocalAttachmentRejectedWithoutNetwork(t *testing.T) {
	s, id, c, in := wikiImageSetup(t)
	ctx := context.Background()
	st, err := s.storeForProfile(id)
	if err != nil {
		t.Fatal(err)
	}
	// 要求IDと同名の添付を別Wikiへ移し、要求Wikiには別IDの添付だけを残す。
	err = st.WithTx(ctx, func(tx *sql.Tx) error {
		if err := store.UpsertWikiList(ctx, tx, &store.Wiki{ID: 11, ProjectID: 1, Name: "Other", Updated: "T1", TagsJSON: "[]", RawJSON: `{}`}, "now"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE wiki_attachments SET wiki_id=11 WHERE wiki_id=10 AND id=7`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeGets, beforeCalls := c.gets, c.calls.Load()
	r, err := s.GetWikiAttachmentImage(ctx, id, in)
	if err != nil || r.Code != "not_own_attachment" {
		t.Fatalf("code=%s err=%v", r.Code, err)
	}
	if c.gets != beforeGets || c.calls.Load() != beforeCalls {
		t.Fatal("local rejection made a detail or download request")
	}
}
