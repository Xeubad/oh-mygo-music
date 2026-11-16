//go:build !windows

package plugin

type platformCredentials struct{}

// 首版只交付 Windows；其他系统明确失败，不静默将秘密降级到明文文件。
func (platformCredentials) get(string) (string, bool, error) {
	return "", false, errCredentialsUnsupported
}

func (platformCredentials) set(string, string) error {
	return errCredentialsUnsupported
}

func (platformCredentials) remove(string) error {
	return errCredentialsUnsupported
}
