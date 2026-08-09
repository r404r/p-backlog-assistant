package backlogclient

import "testing"

func TestValidateSpaceURL_Allowed(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://example.backlog.jp", "https://example.backlog.jp"},
		{"https://example.backlog.com", "https://example.backlog.com"},
		{"https://example.backlog.jp/", "https://example.backlog.jp"},
		{"https://EXAMPLE.Backlog.JP", "https://example.backlog.jp"},
		{"  https://example.backlog.jp  ", "https://example.backlog.jp"},
		{"https://sub.team.backlog.com", "https://sub.team.backlog.com"},
	}
	for _, c := range cases {
		got, err := ValidateSpaceURL(c.in)
		if err != nil {
			t.Errorf("ValidateSpaceURL(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ValidateSpaceURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateSpaceURL_Rejected(t *testing.T) {
	cases := []string{
		"",
		"http://example.backlog.jp",            // HTTPS 必須
		"https://backlog.jp",                   // サブドメイン無し
		"https://backlog.com",                  // サブドメイン無し
		"https://.backlog.jp",                  // 空サブドメイン
		"https://example.backlog.jp.evil.com",  // サフィックス偽装
		"https://evil.com",                     // 許可外ホスト
		"https://examplebacklog.jp",            // ドット無し偽装
		"https://example.backlog.jp:8443",      // ポート付き
		"https://user:pass@example.backlog.jp", // 認証情報付き
		"https://example.backlog.jp/path",      // パス付き
		"https://example.backlog.jp?apiKey=x",  // クエリ付き
		"https://example.backlog.jp#frag",      // フラグメント付き
		"ftp://example.backlog.jp",             // スキーム不正
		"example.backlog.jp",                   // スキーム無し
	}
	for _, in := range cases {
		if got, err := ValidateSpaceURL(in); err == nil {
			t.Errorf("ValidateSpaceURL(%q) = %q, want error", in, got)
		}
	}
}

func TestSpaceHost(t *testing.T) {
	host, err := SpaceHost("https://example.backlog.jp/")
	if err != nil {
		t.Fatal(err)
	}
	if host != "example.backlog.jp" {
		t.Errorf("SpaceHost = %q, want example.backlog.jp", host)
	}
}
