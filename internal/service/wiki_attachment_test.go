package service

import (
 "context"
 "database/sql"
 "io"
 "os"
 "path/filepath"
 "testing"
 "backlog-assistant/internal/backlogclient"
 "backlog-assistant/internal/store"
)

type attachmentConnector struct { *fakeConnector; calls int }
func (c *attachmentConnector) DownloadWikiAttachmentWithProgress(ctx context.Context, _, _ int64, w io.Writer, progress func(int64,int64)) (backlogclient.AttachmentTransfer,error) {
 c.calls++; progress(0,4); n,err:=io.WriteString(w,"data"); return backlogclient.AttachmentTransfer{ReceivedBytes:int64(n),ContentLength:4},err
}
func attachmentService(t *testing.T) (*ProfileService,*attachmentConnector,*store.Store) {
 t.Helper(); ctx:=context.Background(); c:=&attachmentConnector{fakeConnector:&fakeConnector{info:testInfo()}}
 s,_,_:=newTestService(t,c.fakeConnector); s.newClient=func(string,string)(connector,error){return c,nil}
 saved,err:=s.SaveProfile(ctx,"","架空","https://example.backlog.jp","DUMMY-KEY"); if err!=nil {t.Fatal(err)}
 if err=s.SetActiveProfile(saved.Profile.ID);err!=nil {t.Fatal(err)}
 st,err:=s.storeForProfile(saved.Profile.ID);if err!=nil {t.Fatal(err)}
 if err=st.UpsertProject(ctx,&store.Project{ID:1,ProjectKey:"SAMPLE",RawJSON:`{"useWiki":true}`});err!=nil {t.Fatal(err)}
 err=st.WithTx(ctx,func(tx *sql.Tx)error{
  if e:=store.UpsertWikiList(ctx,tx,&store.Wiki{ID:2,ProjectID:1,Name:"架空",Updated:"T1",TagsJSON:"[]",RawJSON:"{}"},"now");e!=nil{return e}
  return store.SaveWikiContent(ctx,tx,2,"sample","T1","now",[]store.WikiAttachment{{ID:3,Name:"CON.txt",Size:4}})
 }); if err!=nil {t.Fatal(err)}
 return s,c,st
}
func TestAttachmentOwnershipDialogAndOriginalBytes(t *testing.T) {
 s,c,_:=attachmentService(t);ctx:=context.Background()
 if ticket:=s.BeginWikiAttachmentDownload(ctx,9,2,3);ticket.Code!="not_found" {t.Fatal(ticket)}
 ticket:=s.BeginWikiAttachmentDownload(ctx,1,2,3);if ticket.Code!="" || ticket.RunID=="" {t.Fatal(ticket)}
 if got:=s.BeginWikiAttachmentDownload(ctx,1,2,3);got.Code!="busy" {t.Fatal(got)}
 result:=s.DownloadWikiAttachment(ctx,1,2,3,func(name string)(string,error){if name!="CON_.txt" {t.Fatal(name)};return "",nil},nil)
 if result.State!="cancelled" || c.calls!=0 {t.Fatal(result,c.calls)}
 target:=filepath.Join(t.TempDir(),"sample.txt")
 s.BeginWikiAttachmentDownload(ctx,1,2,3)
 result=s.DownloadWikiAttachment(ctx,1,2,3,func(string)(string,error){return target,nil},nil)
 data,_:=os.ReadFile(target)
 if result.State!="saved" || string(data)!="data" || c.calls!=1 {t.Fatal(result,string(data),c.calls)}
}
func TestAttachmentSecondOwnershipCheckAndMismatch(t *testing.T) {
 s,c,st:=attachmentService(t);ctx:=context.Background(); target:=filepath.Join(t.TempDir(),"sample.txt")
 s.BeginWikiAttachmentDownload(ctx,1,2,3)
 result:=s.DownloadWikiAttachment(ctx,1,2,3,func(string)(string,error){_,err:=st.DB().Exec(`UPDATE wiki_attachments SET size=5 WHERE id=3`);return target,err},nil)
 if result.Code!="size_mismatch" || c.calls!=1 {t.Fatal(result,c.calls)}
 if _,err:=os.Stat(target);!os.IsNotExist(err) {t.Fatal(err)}
 s.BeginWikiAttachmentDownload(ctx,1,2,3)
 result=s.DownloadWikiAttachment(ctx,1,2,3,func(string)(string,error){_,err:=st.DB().Exec(`UPDATE wikis SET deleted=1 WHERE id=2`);return target,err},nil)
 if result.Code!="not_found" || c.calls!=1 {t.Fatal(result,c.calls)}
}
