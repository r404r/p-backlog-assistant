package service

import (
 "bytes"
 "image"
 "image/color"
 "image/gif"
 "image/jpeg"
 "image/png"
 "testing"
)

func TestNormalizeWikiImage(t *testing.T) {
 img := image.NewRGBA(image.Rect(0, 0, 4, 3))
 img.Set(0, 0, color.RGBA{255, 0, 0, 255})
 for _, format := range []string{"png", "jpeg", "gif"} {
  t.Run(format, func(t *testing.T) {
   var b bytes.Buffer
   switch format { case "png": _ = png.Encode(&b,img); case "jpeg": _ = jpeg.Encode(&b,img,nil); case "gif": _ = gif.Encode(&b,img,nil) }
   r := normalizeWikiImage(b.Bytes())
   if r.Code != "" || r.Width != 4 || r.Height != 3 || r.DataBase64 == "" { t.Fatalf("unexpected result: code=%s %dx%d",r.Code,r.Width,r.Height) }
   if format == "gif" && r.MIME != "image/png" { t.Fatal("GIF must become a still PNG") }
  })
 }
 for _, data := range [][]byte{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), []byte("<html>secret</html>"), {137,80,78,71,13,10,26,10}, make([]byte,(8<<20)+1)} {
  r := normalizeWikiImage(data)
  if r.Code == "" || r.DataBase64 != "" || r.MIME != "" { t.Fatal("invalid image leaked data") }
 }
}
