package plugin

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credentialGeneric      = 1
	credentialLocalMachine = 2
)

var (
	credentialDLL   = windows.NewLazySystemDLL("advapi32.dll")
	credentialRead  = credentialDLL.NewProc("CredReadW")
	credentialWrite = credentialDLL.NewProc("CredWriteW")
	credentialFree  = credentialDLL.NewProc("CredFree")
)

// 与 CREDENTIALW 保持原生字段顺序，让 Go 按目标体系结构补齐指针对齐。
type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         unsafe.Pointer
	TargetAlias        *uint16
	UserName           *uint16
}

type platformCredentials struct{}

func (platformCredentials) get(target string) (string, bool, error) {
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", false, errCredentialsUnavailable
	}
	if err := credentialRead.Find(); err != nil {
		return "", false, errCredentialsUnavailable
	}
	if err := credentialFree.Find(); err != nil {
		return "", false, errCredentialsUnavailable
	}
	var credential *windowsCredential
	ok, _, callErr := credentialRead.Call(uintptr(unsafe.Pointer(name)), credentialGeneric, 0, uintptr(unsafe.Pointer(&credential)))
	runtime.KeepAlive(name)
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return "", false, nil
		}
		return "", false, errCredentialsUnavailable
	}
	if credential == nil {
		return "", false, errCredentialsUnavailable
	}
	defer credentialFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlobSize > credentialBlobLimit || (credential.CredentialBlobSize != 0 && credential.CredentialBlob == nil) {
		return "", false, errCredentialsUnavailable
	}
	// GENERIC blob 由应用定义，这里按 UTF-8 原样保存，支持 Cookie 和 JSON 字符串。
	blob := unsafe.Slice(credential.CredentialBlob, int(credential.CredentialBlobSize))
	value := string(blob)
	clear(blob)
	return value, true, nil
}

func (platformCredentials) set(target, value string) error {
	if len(value) > credentialBlobLimit {
		return errCredentialsUnavailable
	}
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return errCredentialsUnavailable
	}
	if err := credentialWrite.Find(); err != nil {
		return errCredentialsUnavailable
	}
	blob := []byte(value)
	defer clear(blob)
	credential := windowsCredential{
		Type: credentialGeneric, TargetName: name, Persist: credentialLocalMachine,
		CredentialBlobSize: uint32(len(blob)),
	}
	if len(blob) != 0 {
		credential.CredentialBlob = &blob[0]
	}
	// 仅当前 Windows 用户在本机跨登录保留，不使用企业漫游或普通文件替代。
	ok, _, _ := credentialWrite.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(credential)
	runtime.KeepAlive(name)
	runtime.KeepAlive(blob)
	if ok == 0 {
		return errCredentialsUnavailable
	}
	return nil
}

func (platformCredentials) remove(target string) error {
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return errCredentialsUnavailable
	}
	deleteCredential := credentialDLL.NewProc("CredDeleteW")
	if err := deleteCredential.Find(); err != nil {
		return errCredentialsUnavailable
	}
	ok, _, callErr := deleteCredential.Call(uintptr(unsafe.Pointer(name)), credentialGeneric, 0)
	runtime.KeepAlive(name)
	// 重复退出可安全重试；不存在与真正不可访问必须区分。
	if ok == 0 && !errors.Is(callErr, windows.ERROR_NOT_FOUND) {
		return errCredentialsUnavailable
	}
	return nil
}
