package plugin

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// 签名可能位于路径而非查询参数，诊断中遮蔽完整地址，不能只删除 token 字段。
var diagnosticAddress = regexp.MustCompile(`(?i)\b(?:https?|wss?|file)://[^\s<>"']+`)

// diagnosticError 只改变可展示文本；保留错误链，不能因脱敏改变宿主的错误分类。
type diagnosticError struct {
	err      error
	redactor *diagnosticRedactor
}

func (e diagnosticError) Error() string { return e.redactor.redact(e.err.Error()) }
func (e diagnosticError) Unwrap() error { return e.err }

// 只保护诊断出口，不改写脚本返回数据或实际请求；凭据不写入普通配置。
type diagnosticRedactor struct {
	mu      sync.Mutex
	secrets map[string]bool
}

func (d *diagnosticRedactor) remember(secret string) {
	if secret == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.secrets == nil {
		d.secrets = make(map[string]bool)
	}
	rememberText := func(value string) {
		if value == "" {
			return
		}
		d.secrets[value] = true
		// stderr 会按行记录，完整凭据被拆开后，各行仍不能重新成为可输出文本。
		for _, line := range strings.FieldsFunc(value, func(r rune) bool { return r == '\r' || r == '\n' }) {
			d.secrets[line] = true
		}
		// 登录脚本会拆出 Cookie 的名值对或令牌，不能只保护完整 Cookie 串。
		for _, part := range strings.Split(value, ";") {
			pair := strings.TrimSpace(part)
			name, token, ok := strings.Cut(pair, "=")
			if !ok || name == "" || strings.ContainsAny(name, "()<>@,;:\"/[]?={} \t\r\n") {
				continue
			}
			token = strings.Trim(strings.TrimSpace(token), "\"")
			if token != "" {
				d.secrets[pair] = true
				d.secrets[token] = true
			}
		}
	}
	// 轮换或退出后旧异步任务仍可能报错，旧值在本插件对象存活期间继续受保护。
	rememberText(secret)
	// 凭据库允许保存 JSON blob，脚本拆出其中的登录字段后仍须保护原文。
	var decoded any
	if json.Unmarshal([]byte(secret), &decoded) == nil {
		var rememberFields func(any)
		rememberFields = func(value any) {
			switch value := value.(type) {
			case string:
				rememberText(value)
			case map[string]any:
				for _, field := range value {
					rememberFields(field)
				}
			case []any:
				for _, field := range value {
					rememberFields(field)
				}
			}
		}
		rememberFields(decoded)
	}
}

func (d *diagnosticRedactor) redact(text string) string {
	d.mu.Lock()
	secrets := make([]string, 0, len(d.secrets))
	for secret := range d.secrets {
		secrets = append(secrets, secret)
	}
	d.mu.Unlock()
	// 长值先替换，防止短凭据是长凭据前缀时留下后半段。
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	return diagnosticAddress.ReplaceAllString(text, "[REDACTED URL]")
}
