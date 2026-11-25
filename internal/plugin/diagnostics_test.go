package plugin

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 播放地址可能自带签名而不经过凭据库，诊断不能把完整地址写入异常或日志。
func TestPlaybackDiagnosticsDoNotExposeSignedAddresses(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	p := load(t, plain, `
export function home() {
 const address = "https://media.invalid/private-recording.flac?signature=synthetic-playback-signature";
 console.error("media open failed", address);
 throw Object.assign(new Error("cannot open audio: " + address), {code:"unavailable"});
}
`)
	_, err := p.Home()
	if !errors.Is(err, music.ErrUnavailable) {
		t.Fatal("地址脱敏丢失歌曲不可用分类")
	}
	for _, output := range []string{err.Error(), logs.String()} {
		if strings.Contains(output, "media.invalid") || strings.Contains(output, "private-recording.flac") || strings.Contains(output, "synthetic-playback-signature") {
			t.Fatal("解析异常或日志暴露完整播放地址及签名")
		}
	}
	if !strings.Contains(music.Reason(err), "cannot open audio") || !strings.Contains(logs.String(), "media open failed") {
		t.Fatal("地址脱敏丢失安全诊断上下文")
	}
}

// 顶层初始化失败不经过导出函数的异常回调，启动错误也不能暴露播放地址。
func TestPlaybackDiagnosticsRedactStartupFailure(t *testing.T) {
	p := load(t, plain, `
throw new Error("startup media failed: https://media.invalid/startup-recording.flac?signature=synthetic-startup-signature");
export function home() { return {}; }
`)
	_, err := p.Home()
	if err == nil {
		t.Fatal("启动失败被错误地当成解析成功")
	}
	for _, sensitive := range []string{"media.invalid", "startup-recording.flac", "synthetic-startup-signature"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatal("启动异常泄漏播放地址或签名")
		}
	}
	if !strings.Contains(err.Error(), "startup media failed") {
		t.Fatal("启动异常脱敏丢失安全诊断上下文")
	}
}


// 同一行超过管道读取缓冲后会拆成多次 Write，后续片段不能绕过地址脱敏。
func TestPlaybackDiagnosticsRedactChunkedProgramOutput(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(echoEnv, "diagnostic")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	p := load(t, `{"id":"x","permissions":{"process":true}}`, `
export async function suggest({query}) {
 const proc = host.spawn(host.setting("program"), []);
 proc.write(query + "\n");
 const first = await proc.readLine();
 proc.write("program completed\n");
 proc.closeStdin();
 const last = await proc.readLine();
 await proc.wait();
 const expected = (query + "\nprogram completed\n").slice(-8192);
 if (proc.errorOutput() !== expected) throw new Error("stderr communication changed");
 return [first, last];
}
`)
	if err := p.Grant(true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSetting("program", self); err != nil {
		t.Fatal(err)
	}
	query := "program failed https://media.invalid/" + strings.Repeat("private-address-fragment/", 4096) + "recording.flac?signature=synthetic-chunk-signature"
	got, err := p.Suggest(query)
	if err != nil || len(got) != 2 || got[0] != "echo:"+query || got[1] != "echo:program completed" {
		t.Fatal("分块诊断保护改变了子进程通信")
	}
	for _, sensitive := range []string{"media.invalid", "private-address-fragment", "synthetic-chunk-signature"} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatal("分块子进程日志泄漏签名地址片段")
		}
	}
	if !strings.Contains(logs.String(), "program completed") {
		t.Fatal("长行后的安全日志丢失")
	}
}

