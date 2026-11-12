package main

import (
	"context"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"backlog-assistant/internal/service"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

func (a *App) BeginWikiEdit(profile string, projectID, id int64) (*service.WikiEdit, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.BeginWikiEdit(a.ctx, profile, projectID, id)
}
func (a *App) SaveWikiEdit(profile string, in service.WikiSaveInput) (*service.WikiWriteResult, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.SaveWikiEdit(a.ctx, profile, in)
}
func (a *App) CreateWiki(profile string, in service.WikiCreateInput) (*service.WikiWriteResult, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.CreateWiki(a.ctx, profile, in)
}

// WebView の beforeunload に依存せず、OS の終了経路でも未保存を確認する。
func (a *App) SetWikiWindowState(dirty, busy bool, language string) {
	a.wikiWindowMu.Lock()
	defer a.wikiWindowMu.Unlock()
	a.wikiDirty = dirty
	a.wikiBusy = busy
	a.wikiLanguage = language
}
func (a *App) beforeClose(ctx context.Context) bool {
	a.wikiWindowMu.Lock()
	dirty, busy, language := a.wikiDirty, a.wikiBusy, a.wikiLanguage
	a.wikiWindowMu.Unlock()
	if busy {
		return true
	}
	if !dirty {
		return false
	}
	title, message, discard, cancel := "Wiki の未保存の変更", "Wiki の未保存の変更を破棄して終了しますか？", "破棄して終了", "キャンセル"
	if language == "en" {
		title, message, discard, cancel = "Unsaved Wiki changes", "Discard unsaved Wiki changes and quit?", "Discard and quit", "Cancel"
	}
	answer, err := wailsruntime.MessageDialog(ctx, wailsruntime.MessageDialogOptions{Type: wailsruntime.WarningDialog, Title: title, Message: message, Buttons: []string{discard, cancel}, DefaultButton: cancel, CancelButton: cancel})
	return err != nil || answer != discard
}

// Wiki バインディングはページ名・本文・出力先を動作ログに渡さない。
func (a *App) ListWikis(profile string, filter store.WikiFilter) (*service.WikiList, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.ListWikis(a.ctx, profile, filter)
}
func (a *App) SyncWikis(profile string, projectID int64, runID string) (*syncpkg.WikiResult, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.SyncWikis(a.ctx, profile, projectID, runID)
}
func (a *App) RefreshWiki(profile string, projectID, id int64) (*service.WikiPage, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.RefreshWiki(a.ctx, profile, projectID, id)
}
func (a *App) PrepareWikiExport(profile string, projectID int64, subtree string, syncFirst bool, runID string) (*service.WikiExportEstimate, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.PrepareWikiExport(a.ctx, profile, projectID, subtree, syncFirst, runID)
}
func (a *App) CancelWikiExport(profile, runID string) error {
	s, err := a.svc()
	if err != nil {
		return err
	}
	s.CancelWikiExport(profile, runID)
	return nil
}
func (a *App) ExportWiki(profile, runID string) (*ExportResultDTO, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	parent, err := wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{})
	if err != nil || parent == "" {
		s.CancelWikiExport(profile, runID)
		return &ExportResultDTO{}, err
	}
	path, err := s.ExportWiki(profile, runID, parent, version)
	if err != nil {
		return nil, err
	}
	return &ExportResultDTO{Path: path}, nil
}

func (a *App) GetWikiAttachmentImage(profile string, in service.WikiImageInput) (*service.WikiImageResult, error) {
	s, err := a.svc()
	if err != nil {
		return nil, err
	}
	return s.GetWikiAttachmentImage(a.ctx, profile, in)
}
func (a *App) CancelWikiImageLoads(profile, requestID string) error {
	s, err := a.svc()
	if err != nil {
		return err
	}
	s.CancelWikiImageLoads(profile, requestID)
	return nil
}
