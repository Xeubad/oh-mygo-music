package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log"
	"os"
	"runtime"
	"strings"
	"testing"

	"mygo-music/internal/music"
)

// 凭据通过真实脚本保存后，重新加载插件仍可读取，而不依赖普通 storage。
func TestCredentialsSurvivePluginReload(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	previous := systemCredentials
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previous })

	dir, data := write(t, plain, `
export function signIn() {
  host.credentials.set("resolver-key", "synthetic-secret");
  return {};
}
export function home() {
  if (host.credentials.get("resolver-key") !== "synthetic-secret") {
    throw new Error("saved credential was lost");
  }
  return {};
}`)
	p, err := Load(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.SignIn(); err != nil {
		p.Close()
		t.Fatal(err)
	}
	p.Close()

	p, err = Load(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err := p.Home(); err != nil {
		t.Fatal(err)
	}
}

// 主动退出通过真实脚本删除凭据，之后首页不能继续取得旧登录能力。
func TestCredentialsRemovedOnSignOut(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	previous := systemCredentials
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previous })
	p := load(t, plain, `
export function signIn() {
  host.credentials.set("wy-account", "synthetic-cookie");
  return {};
}
export function signOut() {
  host.credentials.remove("wy-account");
}
export function home() {
  if (host.credentials.get("wy-account") !== undefined) {
    throw new Error("signed-out credentials remain accessible");
  }
  return {};
}`)
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	if err := p.SignOut(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Home(); err != nil {
		t.Fatal(err)
	}
}

// 系统库暂不可读时不得用新输入覆盖原凭据，恢复后仍能读到原值。
func TestCredentialsUnreadableStoreCannotBeOverwritten(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	previous := systemCredentials
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previous })
	p := load(t, plain, `
let attempts = 0;
export function signIn() {
  host.credentials.set("resolver-key", ++attempts === 1 ? "original-secret" : "temporary-secret");
  return {};
}
export function home() {
  if (host.credentials.get("resolver-key") !== "original-secret") {
    throw new Error("unreadable store lost original credential");
  }
  return {};
}`)
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	store.readErr = errors.New("synthetic credential store cannot be read")
	if _, err := p.SignIn(); err == nil {
		t.Fatal("unreadable credential store accepted an overwrite")
	}
	store.readErr = nil
	if _, err := p.Home(); err != nil {
		t.Fatal(err)
	}
}

// 库不可读时退出不得删除未知的原凭据，错误必须交给调用方处理。
func TestCredentialsUnreadableStoreCannotBeRemoved(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	previous := systemCredentials
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previous })
	p := load(t, plain, `
export function signIn() {
  host.credentials.set("resolver-key", "original-secret");
  return {};
}
export function signOut() { host.credentials.remove("resolver-key"); }
export function home() {
  if (host.credentials.get("resolver-key") !== "original-secret") {
    throw new Error("unreadable store deleted original credential");
  }
  return {};
}`)
	if _, err := p.SignIn(); err != nil {
		t.Fatal(err)
	}
	store.readErr = errors.New("synthetic credential store cannot be read")
	if err := p.SignOut(); err == nil {
		t.Fatal("unreadable credential store accepted deletion")
	}
	store.readErr = nil
	if _, err := p.Home(); err != nil {
		t.Fatal(err)
	}
}

// 缺参、非字符串、空键及超限秘密不能被强制转换后写入系统凭据库。
func TestCredentialsRejectInvalidArguments(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	previous := systemCredentials
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previous })
	p := load(t, plain, `
export function home() {
  const invalid = [
    () => host.credentials.get(),
    () => host.credentials.get({}),
    () => host.credentials.get(""),
    () => host.credentials.set("key"),
    () => host.credentials.set("key", {}),
    () => host.credentials.set("key", "x".repeat(2561)),
    () => host.credentials.remove(null)
  ];
  for (const call of invalid) {
    let rejected = false;
    try { call(); } catch (_) { rejected = true; }
    if (!rejected) throw new Error("invalid credential argument accepted");
  }
  if (host.credentials.get("key") !== undefined) {
    throw new Error("invalid argument modified credentials");
  }
  return {};
}`)
	if _, err := p.Home(); err != nil {
		t.Fatal(err)
	}
}

// 同名凭据按插件隔离，键区分大小写，秘密不进入普通 storage/settings。
func TestCredentialsAreIsolatedFromOtherPluginsAndStorage(t *testing.T) {
	store := &memoryCredentials{values: map[string]string{}}
	previous := systemCredentials
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previous })
	source := `
export function signIn() {
  host.credentials.set("Resolver", "isolated-synthetic-secret");
  return {};
}
export function home() {
  if (host.credentials.get("Resolver") !== undefined || host.credentials.get("resolver") !== undefined) {
    throw new Error("foreign credential became accessible");
  }
  return {};
}
export function signOut() { host.credentials.remove("Resolver"); }
export function account() {
  if (host.credentials.get("Resolver") !== "isolated-synthetic-secret" || host.credentials.get("resolver") !== undefined) {
    throw new Error("credential key identity changed");
  }
  if (host.storage.get("Resolver") !== undefined || host.setting("Resolver") !== "") {
    throw new Error("secret reached ordinary storage");
  }
  return null;
}`
	a := load(t, plain, source)
	b := load(t, `{"id":"other"}`, source)
	if _, err := a.SignIn(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Home(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Account(); err != nil {
		t.Fatal(err)
	}
	if err := b.SignOut(); err != nil {
		t.Fatal(err)
	}
}

// 原生失败可能包含敏感上下文，但真实 JS 调用收到的异常不能回显秘密。
func TestCredentialsErrorsDoNotExposeSecrets(t *testing.T) {
	const secret = "synthetic-secret-must-not-appear"
	nativeErr := errors.New("credential backend failed with " + secret)
	store := &memoryCredentials{values: map[string]string{}}
	previous := systemCredentials
	systemCredentials = store
	t.Cleanup(func() { systemCredentials = previous })
	p := load(t, plain, `
export function home() { host.credentials.get("resolver-key"); return {}; }
export function signIn() { host.credentials.set("resolver-key", "temporary-input"); return {}; }
export function signOut() { host.credentials.remove("resolver-key"); }
`)
	check := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("credential backend failure was ignored")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("credential error exposed sensitive backend details")
		}
	}
	store.readErr = nativeErr
	_, err := p.Home()
	check(err)
	store.readErr = nil
	store.writeErr = nativeErr
	_, err = p.SignIn()
	check(err)
	store.removeErr = nativeErr
	check(p.SignOut())
}

// 脚本把已读取凭据带入日志或解析异常时，宿主诊断仍须脱敏并保留错误分类。
func TestCredentialsDoNotLeakThroughPlaybackDiagnostics(t *testing.T) {
	const secret = "synthetic-diagnostic-secret-never-log"
	previous := systemCredentials
	systemCredentials = &memoryCredentials{values: map[string]string{}}
	t.Cleanup(func() { systemCredentials = previous })
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	p := load(t, plain, `
export function signIn() {
 host.credentials.set("resolver", "synthetic-diagnostic-secret-never-log");
 return {};
}
export function resolveCandidate() {
 const key = host.credentials.get("resolver");
 console.error("resolve failed", key);
 throw Object.assign(new Error("key denied: " + key), {code:"playbackBlocked"});
}
`)
	if _, err := p.SignIn(); err != nil {
		t.Fatal("合成凭据保存失败")
	}
	_, err := p.ResolveCandidate(context.Background(), "one", "diagnostic-round", music.PlaybackCandidate{ID: "candidate", Platform: "wy", TrackID: "one", Quality: "flac"})
	if !errors.Is(err, music.ErrPlaybackBlocked) {
		t.Fatal("脱敏不能丢失全局封锁分类")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatal("已读取凭据泄漏到解析异常或宿主日志")
	}
	if !strings.Contains(music.Reason(err), "key denied") || !strings.Contains(logs.String(), "resolve failed") {
		t.Fatal("脱敏不能丢弃全部安全诊断上下文")
	}
}

// 登录凭据常以 JSON 保存；脚本拆出 Cookie 或 token 后，诊断仍不能泄漏字段原文。
func TestCredentialsStructuredValuesStayPrivate(t *testing.T) {
	previous := systemCredentials
	systemCredentials = &memoryCredentials{values: map[string]string{}}
	t.Cleanup(func() { systemCredentials = previous })
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	p := load(t, plain, `
export function signIn() {
 host.credentials.set("account", JSON.stringify({cookie:"sid=synthetic-structured-cookie", session:{token:"synthetic-structured-token"}}));
 return {};
}
export function resolveCandidate() {
 const saved = JSON.parse(host.credentials.get("account"));
 console.error("account request failed", saved.cookie);
 throw Object.assign(new Error("account denied: " + saved.session.token), {code:"signIn"});
}
`)
	if _, err := p.SignIn(); err != nil {
		t.Fatal("合成结构化凭据保存失败")
	}
	_, err := p.ResolveCandidate(context.Background(), "one", "diagnostic-round", music.PlaybackCandidate{ID: "candidate", Platform: "wy", TrackID: "one", Quality: "flac"})
	if !errors.Is(err, music.ErrSignIn) {
		t.Fatal("结构化凭据脱敏丢失登录错误分类")
	}
	for _, output := range []string{err.Error(), logs.String()} {
		if strings.Contains(output, "synthetic-structured-cookie") || strings.Contains(output, "synthetic-structured-token") {
			t.Fatal("拆出的登录凭据泄漏到异常或日志")
		}
	}
	if !strings.Contains(music.Reason(err), "account denied") || !strings.Contains(logs.String(), "account request failed") {
		t.Fatal("结构化凭据脱敏丢失安全诊断上下文")
	}
}

// 网页登录返回的 Cookie 在保存前就可能被记录，宿主应先保护诊断而不改写登录结果。
func TestCredentialsWebSignInDiagnosticsStayPrivate(t *testing.T) {
	const cookie = "SID=synthetic-web-login-cookie"
	previousSignIn := webSignIn
	SetWebSignIn(func(WebSignIn) (string, error) { return cookie, nil })
	t.Cleanup(func() { SetWebSignIn(previousSignIn) })
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	p := load(t, `{"id":"x","permissions":{"network":["login.invalid"]}}`, `
let saved;
export async function signIn() {
 const {cookie} = await host.webSignIn({url:"https://login.invalid/login", cookiesOf:"https://login.invalid/", until:["SID"]});
 saved = cookie;
 console.error("login response received", cookie);
 return {id:cookie, name:"synthetic account"};
}
export function resolveCandidate() {
 throw Object.assign(new Error("login rejected: " + saved), {code:"signIn"});
}
`)
	account, err := p.SignIn()
	if err != nil || account.ID != cookie {
		t.Fatal("诊断保护改变了网页登录返回值")
	}
	_, err = p.ResolveCandidate(context.Background(), "one", "diagnostic-round", music.PlaybackCandidate{ID: "candidate", Platform: "wy", TrackID: "one", Quality: "flac"})
	if !errors.Is(err, music.ErrSignIn) {
		t.Fatal("网页登录诊断丢失登录错误分类")
	}
	for _, output := range []string{err.Error(), logs.String()} {
		if strings.Contains(output, "synthetic-web-login-cookie") {
			t.Fatal("网页登录返回的 Cookie 泄漏到异常或日志")
		}
	}
	if !strings.Contains(music.Reason(err), "login rejected") || !strings.Contains(logs.String(), "login response received") {
		t.Fatal("网页登录脱敏丢失安全诊断上下文")
	}
}

// Cookie 常被脚本拆成单项或令牌使用，两种登录入口都不能只保护完整串。
func TestCredentialsCookiePartsStayPrivate(t *testing.T) {
	const cookie = "SID=synthetic-cookie-session; CSRF=synthetic-cookie-csrf"
	for _, entry := range []struct {
		name string
		read string
	}{
		{"web", `const {cookie} = await host.webSignIn({url:"https://login.invalid/login", cookiesOf:"https://login.invalid/", until:["SID"]});`},
		{"stored", `host.credentials.set("account", JSON.stringify({cookie:"SID=synthetic-cookie-session; CSRF=synthetic-cookie-csrf"})); const {cookie} = JSON.parse(host.credentials.get("account"));`},
	} {
		t.Run(entry.name, func(t *testing.T) {
			previousStore := systemCredentials
			systemCredentials = &memoryCredentials{values: map[string]string{}}
			t.Cleanup(func() { systemCredentials = previousStore })
			previousSignIn := webSignIn
			SetWebSignIn(func(WebSignIn) (string, error) { return cookie, nil })
			t.Cleanup(func() { SetWebSignIn(previousSignIn) })
			var logs bytes.Buffer
			previousOutput := log.Writer()
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(previousOutput) })
			p := load(t, `{"id":"x","permissions":{"network":["login.invalid"]}}`, `
export async function signIn() {
 `+entry.read+`
 console.error("cookie request failed", cookie.split(";")[0]);
 const csrf = cookie.split(";")[1].trim().split("=")[1];
 throw Object.assign(new Error("cookie rejected: " + csrf), {code:"signIn"});
}
`)
			_, err := p.SignIn()
			if !errors.Is(err, music.ErrSignIn) {
				t.Fatal("Cookie 拆分脱敏丢失登录错误分类")
			}
			for _, output := range []string{err.Error(), logs.String()} {
				if strings.Contains(output, "synthetic-cookie-session") || strings.Contains(output, "synthetic-cookie-csrf") {
					t.Fatal("Cookie 拆出的单项或令牌泄漏到异常或日志")
				}
			}
			if !strings.Contains(music.Reason(err), "cookie rejected") || !strings.Contains(logs.String(), "cookie request failed") {
				t.Fatal("Cookie 拆分脱敏丢失安全诊断上下文")
			}
		})
	}
}

// 临时解析密钥不经过系统凭据库，也必须在进入脚本前纳入诊断保护。
func TestCredentialsTemporaryResolverKeyDiagnostics(t *testing.T) {
	const secret = "synthetic-temporary-key-never-log"
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	p := load(t, plain, `
export function aggregate({input}) {
 console.warn("temporary key rejected", input.value);
 throw Object.assign(new Error("cannot use temporary key: " + input.value), {code:"setup"});
}
`)
	_, err := p.Aggregate("setResolverKey", map[string]any{"value": secret, "temporary": true})
	if !errors.Is(err, music.ErrSetup) {
		t.Fatal("临时密钥错误的设置分类丢失")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
		t.Fatal("临时解析密钥泄漏到脚本异常或宿主日志")
	}
	if !strings.Contains(music.Reason(err), "cannot use temporary key") || !strings.Contains(logs.String(), "temporary key rejected") {
		t.Fatal("临时密钥脱敏丢失安全诊断上下文")
	}
}

// 原生验证必须显式启用，只触及随机插件命名空间，并在失败时也删除合成凭据。
func TestCredentialsWindowsSmoke(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("MYGO_TEST_WINDOWS_CREDENTIALS") != "1" {
		t.Skip("enable MYGO_TEST_WINDOWS_CREDENTIALS=1 on Windows for native credential smoke test")
	}
	previous := systemCredentials
	systemCredentials = platformCredentials{}
	t.Cleanup(func() { systemCredentials = previous })
	dir, data := write(t, `{"id":"credential-smoke-`+rand.Text()+`"}`, `
const secret = '{"cookie":"合成-Cookie-Only","token":"not-a-real-token"}';
export function signIn() { host.credentials.set("smoke", secret); return {}; }
export function home() {
  if (host.credentials.get("smoke") !== secret) throw new Error("native credential did not survive reload");
  return {};
}
export function signOut() { host.credentials.remove("smoke"); }
export function account() {
  if (host.credentials.get("smoke") !== undefined) throw new Error("native credential was not removed");
  return null;
}`)
	p, err := Load(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	target := p.credentialTarget("smoke")
	t.Cleanup(func() {
		if err := (platformCredentials{}).remove(target); err != nil {
			t.Error("failed to remove isolated native smoke credential")
		}
	})
	if _, err := p.Account(); err != nil {
		p.Close()
		t.Fatal(err)
	}
	if _, err := p.SignIn(); err != nil {
		p.Close()
		t.Fatal(err)
	}
	p.Close()
	p, err = Load(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err := p.Home(); err != nil {
		t.Fatal(err)
	}
	if err := p.SignOut(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Account(); err != nil {
		t.Fatal(err)
	}
}

// 只替换操作系统凭据边界；脚本加载、宿主桥接和公开插件调用保持真实。
type memoryCredentials struct {
	values    map[string]string
	readErr   error
	writeErr  error
	removeErr error
}

func (s *memoryCredentials) get(target string) (string, bool, error) {
	if s.readErr != nil {
		return "", false, s.readErr
	}
	value, ok := s.values[target]
	return value, ok, nil
}

func (s *memoryCredentials) set(target, value string) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.values[target] = value
	return nil
}

func (s *memoryCredentials) remove(target string) error {
	if s.removeErr != nil {
		return s.removeErr
	}
	delete(s.values, target)
	return nil
}
