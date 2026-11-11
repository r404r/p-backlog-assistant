package main

import (
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"backlog-assistant/internal/service"
	"backlog-assistant/internal/store"
	syncpkg "backlog-assistant/internal/sync"
)

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
