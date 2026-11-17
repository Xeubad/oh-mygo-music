package app

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"mygo-music/internal/music"
)

type aggregateSource interface {
	Aggregate(string, map[string]any) (json.RawMessage, error)
	Home() (*music.Browse, error)
}

type aggregateList struct {
	Platform     string `json:"platform"`
	AccountID    string `json:"accountID"`
	ListID       string `json:"listID"`
	ListName     string `json:"listName"`
	RefreshedAt  int64  `json:"refreshedAt"`
	Selected     bool   `json:"selected"`
	RefreshState string `json:"refreshState"`
}

type aggregateRefresh struct {
	State         string `json:"state"`
	Version       *int64 `json:"version"` // 缺失与版本 0 不同，旧插件不能触发无限重取。
	NextRefreshAt int64  `json:"nextRefreshAt"`
}

type aggregateQualityTarget struct {
	Quality   string   `json:"quality"`
	Platforms []string `json:"platforms"`
}

type aggregateReply struct {
	Refresh        aggregateRefresh         `json:"refresh"`
	QualityTargets []aggregateQualityTarget `json:"qualityTargets"`
	Settings       struct {
		TargetQuality           string   `json:"targetQuality"`
		MinimumQuality          string   `json:"minimumQuality"`
		CandidateTimeoutMs      int      `json:"candidateTimeoutMs"`
		RoundTimeoutMs          int      `json:"roundTimeoutMs"`
		StallTimeoutMs          int      `json:"stallTimeoutMs"`
		RecoveryTimeoutMs       int      `json:"recoveryTimeoutMs"`
		ConsecutiveFailureLimit int      `json:"consecutiveFailureLimit"`
		RefreshIntervalMs       int      `json:"refreshIntervalMs"`
		PlatformOrder           []string `json:"platformOrder"`
	} `json:"settings"`
	ResolverCredential struct {
		State string `json:"state"`
	} `json:"resolverCredential"`
	ResolverCredentials map[string]struct {
		State string `json:"state"`
	} `json:"resolverCredentials"`
}

type aggregateView struct {
	platform, list, kind string
	query                string
	selected             []music.AggregateSource
	sourceChoices        map[string]string
	lists                []aggregateList
	listsLoaded          bool
	loadingLists         bool
	busy                 bool
	err                  string
	refresh              aggregateRefresh
	refreshErr           string
	refreshVersion       *int64
	revision             uint64 // 手动操作使在途后台快照失效，避免较旧回执倒写。
	polling              bool
	pollUnsupported      bool
	nextPoll             time.Time
	owner                aggregateSource
	page                 route
}

type aggregateEdit struct {
	loaded, busy             bool
	err                      string
	target, minimum          string
	values                   map[string]string
	order                    []string
	key                      string // 仅编辑期间持有；永远不从凭据库回填到界面。
	credentialState          string
	wyKey, wyCredentialState string // 网易与通用服务分开持有，不能跨服务回填或清空草稿。
	qualityTargets           []aggregateQualityTarget
}

var aggregateTimingFields = []struct{ key, label string }{
	{"candidateTimeoutMs", "Candidate Timeout (ms)"},
	{"roundTimeoutMs", "Round Timeout (ms)"},
	{"stallTimeoutMs", "Stall Detection (ms)"},
	{"recoveryTimeoutMs", "Recovery Budget (ms)"},
	{"consecutiveFailureLimit", "Consecutive Failure Limit"},
	{"refreshIntervalMs", "Refresh Interval (ms)"},
}

func (e *aggregateEdit) apply(reply aggregateReply) {
	s := reply.Settings
	e.target, e.minimum = s.TargetQuality, s.MinimumQuality
	e.order = append([]string(nil), s.PlatformOrder...)
	e.qualityTargets = append([]aggregateQualityTarget(nil), reply.QualityTargets...)
	e.values = map[string]string{
		"candidateTimeoutMs":      strconv.Itoa(s.CandidateTimeoutMs),
		"roundTimeoutMs":          strconv.Itoa(s.RoundTimeoutMs),
		"stallTimeoutMs":          strconv.Itoa(s.StallTimeoutMs),
		"recoveryTimeoutMs":       strconv.Itoa(s.RecoveryTimeoutMs),
		"consecutiveFailureLimit": strconv.Itoa(s.ConsecutiveFailureLimit),
		"refreshIntervalMs":       strconv.Itoa(s.RefreshIntervalMs),
	}
}

// 只经过来源的公开控制接口，界面不接触插件存储文件或系统凭据库。
func (a *app) aggregateCommand(lib *library, e *aggregateEdit, action string, input map[string]any) {
	if e.busy {
		return
	}
	src, ok := lib.src.(aggregateSource)
	if !ok {
		return
	}
	e.busy, e.err = true, ""
	go func() {
		raw, err := src.Aggregate(action, input)
		if err == nil && action != "status" {
			// 写入回执只表示操作成功；再读公开状态，避免把 {ok:true} 当作零值设置回填。
			raw, err = src.Aggregate("status", nil)
		}
		var reply aggregateReply
		if err == nil {
			err = json.Unmarshal(raw, &reply)
		}
		a.post(func() {
			// 关闭或更换来源后的旧回执不能修改另一份设置草稿。
			if a.aggregateEdits[lib] != e {
				return
			}
			e.busy = false
			if err != nil {
				e.loaded = true
				e.err = err.Error()
				return
			}
			if !e.loaded || action == "configure" || action == "status" {
				e.apply(reply)
			}
			e.loaded = true
			e.credentialState = reply.ResolverCredential.State
			e.wyCredentialState = reply.ResolverCredentials["wy"].State
			if action == "setResolverKey" || action == "clearResolverKey" {
				if input["platform"] == "wy" {
					e.wyKey = ""
				} else {
					e.key = ""
				}
			}
		})
	}()
}

// 与其他插件设置复用同一面板，字段按行排列；参数通过一次联合提交生效。
func (a *app) aggregateSettings(c *ui.Context, lib *library, p palette) {
	if !lib.can("aggregate") {
		return
	}
	if _, ok := lib.src.(aggregateSource); !ok {
		return
	}
	if a.aggregateEdits == nil {
		a.aggregateEdits = map[*library]*aggregateEdit{}
	}
	e := a.aggregateEdits[lib]
	if e == nil {
		e = &aggregateEdit{}
		a.aggregateEdits[lib] = e
		a.aggregateCommand(lib, e, "status", nil)
	}
	ui.Column(c).Gap(8).Children(func() {
		if !e.loaded {
			ui.Text(c, T("Loading aggregate settings…")).FontSize(12).TextColor(p.muted)
			return
		}
		if e.values == nil {
			ui.Text(c, T("Aggregate settings unavailable")).FontSize(12).TextColor(p.muted)
			if ui.Button(c.Key("reload"), T("Reload aggregate status")).Disabled(e.busy).Clicked() {
				a.aggregateCommand(lib, e, "status", nil)
			}
			return
		}
		qualities := []string{"flac", "320k", "256k", "128k", "64k", "32k"}
		targets := append([]string(nil), qualities...)
		for _, target := range e.qualityTargets {
			targets = append(targets, target.Quality)
		}
		aggregateField(c, "Target Quality", func() {
			ui.Select(c.Key("target"), &e.target, targets).Label(T("Target Quality")).Width(180).Disabled(e.busy)
		})
		for _, target := range e.qualityTargets {
			ui.Text(c, fmt.Sprintf(T("%s — declared by %s; actual audio is verified before playback."), target.Quality, strings.Join(target.Platforms, ", "))).FontSize(11.5).TextColor(p.muted)
		}
		aggregateField(c, "Minimum Quality", func() {
			ui.Select(c.Key("minimum"), &e.minimum, qualities).Label(T("Minimum Quality")).Width(180).Disabled(e.busy)
		})
		for _, f := range aggregateTimingFields {
			aggregateField(c, f.label, func() {
				value := e.values[f.key]
				ui.TextInput(c.Key(f.key), &value).Label(f.label).Width(180).Disabled(e.busy)
				e.values[f.key] = value
			})
		}
		aggregateField(c, "Platform Order", func() {
			ui.Column(c).Gap(4).Children(func() {
				for i, platform := range e.order {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, platform).FontSize(12).Width(36)
						if ui.Button(c.Key("up-"+platform), T("Move Up")+" "+platform).Disabled(e.busy || i == 0).Clicked() {
							e.order[i-1], e.order[i] = e.order[i], e.order[i-1]
						}
						if ui.Button(c.Key("down-"+platform), T("Move Down")+" "+platform).Disabled(e.busy || i == len(e.order)-1).Clicked() {
							e.order[i+1], e.order[i] = e.order[i], e.order[i+1]
						}
					})
				}
			})
		})
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c.Key("save"), T("Save Aggregate Settings")).Disabled(e.busy).Clicked() {
				input := map[string]any{"targetQuality": e.target, "minimumQuality": e.minimum, "platformOrder": e.order}
				valid := true
				for _, f := range aggregateTimingFields {
					n, err := strconv.Atoi(e.values[f.key])
					if err != nil || n <= 0 {
						e.err = fmt.Sprintf(T("Enter a positive integer for %s"), T(f.label))
						valid = false
						break
					}
					input[f.key] = n
				}
				if valid {
					a.aggregateCommand(lib, e, "configure", input)
				}
			}
			if ui.Button(c.Key("discard"), T("Reload aggregate status")).Disabled(e.busy).Clicked() {
				a.aggregateCommand(lib, e, "status", nil)
			}
			if current := a.pl.Current(); current != nil && current.Source == lib.id {
				// 保存设置不打断播放；只有明确重载才将新设置应用于当前曲目。
				if ui.Button(c.Key("reload-track"), T("Reload Current Track")).Disabled(e.busy).Clicked() {
					a.pl.Reload()
				}
			}
		})
		ui.Text(c, T("Stall detection must be below the recovery budget; changes apply to new playback flows.")).FontSize(11.5).TextColor(p.muted)
		aggregateField(c, "Resolver Key", func() {
			ui.TextInput(c.Key("key"), &e.key).Label(T("Resolver Key")).Password().Width(260).Disabled(e.busy)
		})
		stateText := map[string]string{"saved": T("Saved securely"), "temporary": T("Temporary only"), "unavailable": T("Credential store unavailable; temporary input allowed"), "empty": T("Not saved")}[e.credentialState]
		ui.Text(c, stateText).FontSize(11.5).TextColor(p.muted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c.Key("save-key"), T("Save Resolver Key Securely")).Disabled(e.busy || e.key == "").Clicked() {
				a.aggregateCommand(lib, e, "setResolverKey", map[string]any{"value": e.key, "temporary": false})
			}
			if ui.Button(c.Key("temporary-key"), T("Use Once")).Disabled(e.busy || e.key == "").Clicked() {
				a.aggregateCommand(lib, e, "setResolverKey", map[string]any{"value": e.key, "temporary": true})
			}
		})
		if e.wyCredentialState != "" {
			aggregateField(c, T("NetEase Resolver Key"), func() {
				ui.TextInput(c.Key("wy-key"), &e.wyKey).Label(T("NetEase Resolver Key")).Password().Width(260).Disabled(e.busy)
			})
			ui.Text(c, map[string]string{"saved": T("Saved securely"), "temporary": T("Temporary only"), "unavailable": T("Credential store unavailable; temporary input allowed"), "empty": T("Not saved")}[e.wyCredentialState]).FontSize(11.5).TextColor(p.muted)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c.Key("save-wy-key"), T("Save NetEase Key Securely")).Disabled(e.busy || e.wyKey == "").Clicked() {
					a.aggregateCommand(lib, e, "setResolverKey", map[string]any{"platform": "wy", "value": e.wyKey, "temporary": false})
				}
				if ui.Button(c.Key("temporary-wy-key"), T("Use NetEase Key Once")).Disabled(e.busy || e.wyKey == "").Clicked() {
					a.aggregateCommand(lib, e, "setResolverKey", map[string]any{"platform": "wy", "value": e.wyKey, "temporary": true})
				}
			})
		}
		if e.err != "" {
			ui.Text(c, e.err).FontSize(12).TextColor(p.accent).LineHeight(1.4)
		}
	})
}

func aggregateField(c *ui.Context, label string, control func()) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).MinHeight(36).Children(func() {
		ui.Text(c, label).FontSize(12.5).Width(164)
		control()
	})
}
