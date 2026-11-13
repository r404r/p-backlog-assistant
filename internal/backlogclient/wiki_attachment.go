package backlogclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

type AttachmentTransfer struct {
	ReceivedBytes int64
	ContentLength int64
}
type AttachmentError struct{ Code string }

func (e *AttachmentError) Error() string { return e.Code }

func (c *Client) DownloadWikiAttachmentWithProgress(ctx context.Context, wikiID, attachmentID int64, dst io.Writer, onProgress func(int64, int64)) (out AttachmentTransfer, err error) {
	out.ContentLength = -1
	if wikiID <= 0 || attachmentID <= 0 {
		return out, &AttachmentError{"not_found"}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.DisableCompression = true
	defer transport.CloseIdleConnections()
	ic := newInterceptor(c.limiter)
	ic.base.Transport = transport
	ic.base.Timeout = 0
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v2/wikis/%d/attachments/%d?%s", c.spaceURL, wikiID, attachmentID, url.Values{"apiKey": {c.apiKey}}.Encode()), nil)
	if e != nil {
		return out, &AttachmentError{"network"}
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, e := ic.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return out, context.Cause(ctx)
		}
		switch {
		case errors.Is(e, ErrUnauthorized):
			return out, ErrUnauthorized
		case errors.Is(e, ErrPermissionDenied):
			return out, ErrPermissionDenied
		case errors.Is(e, ErrRateLimitExceeded):
			return out, ErrRateLimitExceeded
		}
		return out, &AttachmentError{"network"}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return out, ErrNotFound
	}
	if resp.StatusCode != 200 || (resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity") {
		return out, &AttachmentError{"network"}
	}
	out.ContentLength = resp.ContentLength
	emit := func() {
		if onProgress != nil {
			onProgress(out.ReceivedBytes, out.ContentLength)
		}
	}
	emit()
	timeout := &AttachmentError{"timeout"}
	timer := time.AfterFunc(30*time.Second, func() { cancel(timeout) })
	defer timer.Stop()
	last := time.Now()
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			timer.Reset(30 * time.Second)
			written, writeErr := dst.Write(buffer[:n])
			out.ReceivedBytes += int64(written)
			if writeErr != nil || written != n {
				return out, &AttachmentError{"disk"}
			}
			if time.Since(last) >= 100*time.Millisecond {
				emit()
				last = time.Now()
			}
		}
		if ctx.Err() != nil {
			return out, context.Cause(ctx)
		}
		if readErr != nil {
			if readErr != io.EOF {
				if errors.Is(readErr, io.ErrUnexpectedEOF) {
					return out, &AttachmentError{"truncated"}
				}
				return out, &AttachmentError{"network"}
			}
			break
		}
	}
	if out.ContentLength >= 0 && out.ReceivedBytes != out.ContentLength {
		return out, &AttachmentError{"truncated"}
	}
	emit()
	return out, nil
}
