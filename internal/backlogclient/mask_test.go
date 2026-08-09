package backlogclient

import (
	"strings"
	"testing"
)

func TestMaskAPIKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			"https://example.backlog.jp/api/v2/users/myself?apiKey=SECRET123",
			"https://example.backlog.jp/api/v2/users/myself?apiKey=***",
		},
		{
			"https://example.backlog.jp/api/v2/issues?apiKey=SECRET123&count=100",
			"https://example.backlog.jp/api/v2/issues?apiKey=***&count=100",
		},
		{
			// 大文字小文字違い
			"https://example.backlog.jp/api/v2/space?APIKEY=abc",
			"https://example.backlog.jp/api/v2/space?APIKEY=***",
		},
		{
			// エラーメッセージ内の URL
			`Get "https://example.backlog.jp/api/v2/users?apiKey=xyz789": dial tcp: timeout`,
			`Get "https://example.backlog.jp/api/v2/users?apiKey=***": dial tcp: timeout`,
		},
		{
			"apiKey 無しの文字列はそのまま",
			"apiKey 無しの文字列はそのまま",
		},
	}
	for _, c := range cases {
		if got := MaskAPIKey(c.in); got != c.want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskAPIKey_NoSecretLeak(t *testing.T) {
	secret := "THIS_IS_A_DUMMY_KEY"
	in := "https://example.backlog.jp/api/v2/rateLimit?apiKey=" + secret
	if strings.Contains(MaskAPIKey(in), secret) {
		t.Error("マスク後の文字列に API キーが残っている")
	}
}
