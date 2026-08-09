package backlogclient

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateSpaceURL はスペース URL を検証し、正規化済み URL("https://<host>")を返す。
//
// セキュリティ要件(設計書 4.1 節):
//   - API キーはクエリパラメータで送信されるため、誤送信防止として HTTPS 必須。
//   - ホストは *.backlog.jp / *.backlog.com のみ許可(サブドメイン必須)。
//   - 認証情報・ポート・パス・クエリ・フラグメント付きの URL は拒否する。
func ValidateSpaceURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("スペース URL が空です")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("スペース URL を解析できません: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("スペース URL は https:// で始まる必要があります(API キー漏えい防止のため HTTP は許可しません)")
	}
	if u.User != nil {
		return "", fmt.Errorf("スペース URL に認証情報を含めることはできません")
	}
	if u.Port() != "" {
		return "", fmt.Errorf("スペース URL にポート番号を含めることはできません")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("スペース URL にクエリやフラグメントを含めることはできません")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("スペース URL にパスを含めることはできません(例: https://example.backlog.jp)")
	}
	host := strings.ToLower(u.Hostname())
	if !isAllowedHost(host) {
		return "", fmt.Errorf("許可されていないホストです(*.backlog.jp / *.backlog.com のみ利用できます)")
	}
	return "https://" + host, nil
}

// isAllowedHost は host が「サブドメイン + .backlog.jp / .backlog.com」かを判定する。
func isAllowedHost(host string) bool {
	for _, suffix := range []string{".backlog.jp", ".backlog.com"} {
		if strings.HasSuffix(host, suffix) {
			sub := strings.TrimSuffix(host, suffix)
			// サブドメイン必須("backlog.jp" 自体や ".backlog.jp" は拒否)
			if sub != "" && !strings.HasPrefix(sub, ".") && !strings.HasSuffix(sub, ".") {
				return true
			}
		}
	}
	return false
}

// SpaceHost は検証済みスペース URL からホスト名を取り出す(DB ファイル名等に使用)。
func SpaceHost(spaceURL string) (string, error) {
	canonical, err := ValidateSpaceURL(spaceURL)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(canonical, "https://"), nil
}
