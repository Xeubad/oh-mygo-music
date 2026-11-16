package plugin

import (
	"bytes"
	"context"
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
export function resolveCandidate() {
 const address = "https://media.invalid/private-recording.flac?signature=synthetic-playback-signature";
 console.error("media open failed", address);
 throw Object.assign(new Error("cannot open audio: " + address), {code:"unavailable"});
}
`)
	_, err := p.ResolveCandidate(context.Background(), "one", "diagnostic-round", music.PlaybackCandidate{ID: "candidate", Platform: "wy", TrackID: "one", Quality: "flac"})
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
export function resolveCandidate() { return {}; }
`)
	_, err := p.ResolveCandidate(context.Background(), "one", "diagnostic-round", music.PlaybackCandidate{ID: "candidate", Platform: "wy", TrackID: "one", Quality: "flac"})
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

// stderr 按行记录时会拆开多行凭据，不能因此把每一段重新当成安全文本。
func TestPlaybackDiagnosticsRedactMultilineCredentials(t *testing.T) {
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
export function aggregate() { return {}; }
export async function suggest({query}) {
 const proc = host.spawn(host.setting("program"), []);
 proc.write(query + "\n");
 proc.closeStdin();
 const lines = [await proc.readLine(), await proc.readLine()];
 await proc.wait();
 if (proc.errorOutput() !== query + "\n") throw new Error("stderr communication changed");
 return lines;
}
`)
	if err := p.Grant(true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSetting("program", self); err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-multiline-first\nsynthetic-multiline-second"
	if _, err := p.Aggregate("setResolverKey", map[string]any{"value": secret, "temporary": true}); err != nil {
		t.Fatal("合成多行凭据设置失败")
	}
	query := "program failed " + secret
	got, err := p.Suggest(query)
	if err != nil || len(got) != 2 || got[0] != "echo:program failed synthetic-multiline-first" || got[1] != "echo:synthetic-multiline-second" {
		t.Fatal("多行诊断保护改变了子进程通信")
	}
	if strings.Contains(logs.String(), "synthetic-multiline-first") || strings.Contains(logs.String(), "synthetic-multiline-second") {
		t.Fatal("多行凭据的片段泄漏到子进程日志")
	}
	if !strings.Contains(logs.String(), "program failed") {
		t.Fatal("多行凭据脱敏丢失安全诊断上下文")
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

// 子进程 stderr 是独立日志出口；脱敏不能改写 stdout 或脚本读取的原始通信。
func TestPlaybackDiagnosticsRedactProgramLogsWithoutChangingCommunication(t *testing.T) {
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
export function aggregate() { return {}; }
export async function suggest({query}) {
 const proc = host.spawn(host.setting("program"), []);
 proc.write(query + "\n");
 proc.closeStdin();
 const line = await proc.readLine();
 await proc.wait();
 if (proc.errorOutput().trim() !== query) throw new Error("stderr communication changed");
 return [line];
}
`)
	if err := p.Grant(true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetSetting("program", self); err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-program-diagnostic-secret"
	if _, err := p.Aggregate("setResolverKey", map[string]any{"value": secret, "temporary": true}); err != nil {
		t.Fatal("合成临时凭据设置失败")
	}
	query := "program failed " + secret + " https://media.invalid/signed-recording.flac?signature=synthetic-program-signature"
	got, err := p.Suggest(query)
	if err != nil || len(got) != 1 || got[0] != "echo:"+query {
		t.Fatal("诊断保护改变了子进程通信")
	}
	for _, sensitive := range []string{secret, "media.invalid", "signed-recording.flac", "synthetic-program-signature"} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatal("子进程诊断日志泄漏凭据或播放地址")
		}
	}
	if !strings.Contains(logs.String(), "program failed") {
		t.Fatal("子进程脱敏丢失安全诊断上下文")
	}
}
