package backlogclient

import (
 "context"
 "errors"
 "io"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
)
func TestAttachmentTransferLengthAndIdentity(t *testing.T) {
 for _, truncated:=range []bool{false,true} {
  srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
   if r.Header.Get("Accept-Encoding")!="identity" {t.Error("identity required")}
   if truncated {w.Header().Set("Content-Length","20")};_,_=io.WriteString(w,"data")
  }))
  var dst strings.Builder
  transfer,err:=newFakeClient(srv.URL).DownloadWikiAttachmentWithProgress(context.Background(),1,2,&dst,func(int64,int64){})
  srv.Close()
  if truncated {var failure *AttachmentError;if !errors.As(err,&failure)||failure.Code!="truncated" {t.Fatal(err)}} else if err!=nil||transfer.ReceivedBytes!=4||dst.String()!="data" {t.Fatal(transfer,err)}
 }
}
