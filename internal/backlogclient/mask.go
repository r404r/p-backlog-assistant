package backlogclient

import "regexp"

// apiKeyPattern は URL・エラーメッセージ中の apiKey パラメータ値にマッチする。
var apiKeyPattern = regexp.MustCompile(`(?i)(apiKey=)[^&\s"']*`)

// MaskAPIKey は文字列中の apiKey パラメータ値を "***" に置換する。
// エラーメッセージ・ログに URL を含める場合は必ずこの関数を通すこと。
func MaskAPIKey(s string) string {
	return apiKeyPattern.ReplaceAllString(s, "${1}***")
}
