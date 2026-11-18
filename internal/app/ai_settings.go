package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"

	"mygo-music/internal/automix"
)

type aiSettingsState struct {
	editing       bool
	draft         automix.Provider
	keyDraft      string
	showKey       bool
	advanced      bool
	confirmDelete bool
	saving        bool
	job           string
	cancel        context.CancelFunc
	epoch         uint64
	status        string
	keyStatus     string
	models        []automix.Model
	modelFilter   string
	temporary     map[string]string
}

func (preferences *settings) migrateAIProviders() {
	if len(preferences.AIProviders) > 0 || strings.TrimSpace(preferences.AutoMixEndpoint) == "" {
		return
	}
	provider := automix.Provider{
		ID: "legacy-automix", Name: "AutoMix", Preset: "custom", Protocol: automix.ProtocolChat,
		Endpoint: preferences.AutoMixEndpoint, Model: preferences.AutoMixModel, TimeoutMs: 3000,
	}
	preferences.AIProviders = []automix.Provider{provider}
	preferences.AutoMixProviderID = provider.ID
	preferences.AutoMixEndpoint, preferences.AutoMixModel = "", ""
}

func aiCredentialKey(provider automix.Provider) string {
	// 旧版本只按接口保存；迁移项保留这一入口，新供应商均使用独立身份。
	if provider.ID == "legacy-automix" {
		return autoMixCredentialKey(provider.Endpoint)
	}
	base, err := automix.BaseEndpoint(provider.Endpoint)
	if err != nil || provider.ID == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(provider.ID + "\n" + base))
	return "music-ai-" + hex.EncodeToString(digest[:16])
}

func (a *app) aiProvider(id string) (automix.Provider, bool) {
	for _, provider := range a.settings.AIProviders {
		if provider.ID == id {
			return provider, true
		}
	}
	return automix.Provider{}, false
}

func (a *app) cancelAIProbe() {
	if a.ai.cancel != nil {
		a.ai.cancel()
		a.ai.cancel = nil
	}
	a.ai.epoch++
	a.ai.job = ""
}

func (a *app) beginAIProvider(id string) {
	a.cancelAIProbe()
	a.ai.editing, a.ai.confirmDelete, a.ai.showKey = true, false, false
	a.ai.keyDraft, a.ai.status, a.ai.keyStatus = "", "", ""
	a.ai.models, a.ai.modelFilter = nil, ""
	provider, ok := a.aiProvider(id)
	if !ok {
		provider = automix.Provider{ID: rand.Text(), Name: T("New provider"), Preset: "custom", Protocol: automix.ProtocolChat, TimeoutMs: 3000}
	}
	a.ai.draft = provider
	if provider.Temperature != nil {
		value := *provider.Temperature
		a.ai.draft.Temperature = &value
	}
	a.ai.advanced = provider.MaxTokens > 0 || provider.Temperature != nil || provider.Effort != ""
	if ok {
		a.readAICredentialStatus()
	}
}

func (a *app) closeAIEditor() {
	a.cancelAIProbe()
	a.ai.editing, a.ai.confirmDelete = false, false
	a.ai.keyDraft, a.ai.showKey = "", false
	a.ai.models = nil
}

func (a *app) aiTemporary(key string) string {
	return a.ai.temporary[key]
}

func readAICredential(key, temporary string) (string, error) {
	if temporary != "" {
		return temporary, nil
	}
	if key == "" {
		return "", errors.New("invalid provider address")
	}
	data, err := getAutoMixSecret(key)
	if errors.Is(err, mygo.ErrSecretNotFound) || errors.Is(err, os.ErrNotExist) || errors.Is(err, errors.ErrUnsupported) {
		return "", nil
	}
	return string(data), err
}

func (a *app) readAICredentialStatus() {
	provider, epoch := a.ai.draft, a.ai.epoch
	key := aiCredentialKey(provider)
	temporary := a.aiTemporary(key)
	go func() {
		value, err := readAICredential(key, temporary)
		a.post(func() {
			if a.ai.epoch != epoch || aiCredentialKey(a.ai.draft) != key {
				return
			}
			switch {
			case err != nil:
				a.ai.keyStatus = T("Could not read saved key")
			case temporary != "":
				a.ai.keyStatus = T("Key available for this session")
			case value != "":
				a.ai.keyStatus = T("Key saved securely")
			default:
				a.ai.keyStatus = T("No saved key; local services may not need one")
			}
		})
	}()
}

func (a *app) saveAIProvider() {
	if a.ai.saving {
		return
	}
	provider := a.ai.draft
	provider.Name, provider.Model = strings.TrimSpace(provider.Name), strings.TrimSpace(provider.Model)
	if base, err := automix.BaseEndpoint(provider.Endpoint); err == nil {
		provider.Endpoint = base
	}
	if err := provider.Validate(); err != nil {
		a.ai.status = T("Check provider name, address and advanced parameters")
		return
	}
	for _, saved := range a.settings.AIProviders {
		if saved.ID != provider.ID && strings.EqualFold(saved.Name, provider.Name) {
			a.ai.status = T("Choose a unique provider name")
			return
		}
	}
	if provider.Temperature != nil {
		value := *provider.Temperature
		provider.Temperature = &value
	}
	a.cancelAIProbe()
	epoch := a.ai.epoch
	key, credential := aiCredentialKey(provider), strings.TrimSpace(a.ai.keyDraft)
	a.ai.saving = true
	go func() {
		var err error
		if credential != "" {
			err = setAutoMixSecret(key, []byte(credential))
		}
		a.post(func() {
			a.ai.saving = false
			if err != nil && !errors.Is(err, errors.ErrUnsupported) {
				if a.ai.epoch == epoch {
					a.ai.status = T("Could not save AI API key; local AutoMix remains available")
				}
				return
			}
			if credential != "" {
				if a.ai.temporary == nil {
					a.ai.temporary = make(map[string]string)
				}
				delete(a.ai.temporary, key)
				if err != nil {
					a.ai.temporary[key] = credential
				}
			}
			original, selected := a.settings.AIProviders, a.settings.AutoMixProviderID
			updated := slices.Clone(original)
			index := slices.IndexFunc(updated, func(item automix.Provider) bool { return item.ID == provider.ID })
			if index < 0 {
				updated = append(updated, provider)
			} else {
				updated[index] = provider
			}
			a.settings.AIProviders = updated
			if selected == "" && len(original) == 0 {
				a.settings.AutoMixProviderID = provider.ID
			}
			if failure := a.settings.save(); failure != nil {
				a.settings.AIProviders, a.settings.AutoMixProviderID = original, selected
				a.ai.status = T("Could not save provider settings")
				return
			}
			if a.ai.epoch == epoch {
				a.ai.draft, a.ai.keyDraft = provider, ""
				a.ai.status = T("Provider saved")
				if err != nil {
					a.ai.keyStatus = T("Key available for this session")
				} else if credential != "" {
					a.ai.keyStatus = T("Key saved securely")
				}
			}
			a.configureAutoMixAdvisor()
		})
	}()
}

func (a *app) selectAIProvider(id string) {
	previous := a.settings.AutoMixProviderID
	a.settings.AutoMixProviderID = id
	if err := a.settings.save(); err != nil {
		a.settings.AutoMixProviderID = previous
		a.ai.status = T("Could not save provider settings")
		return
	}
	a.configureAutoMixAdvisor()
}

func (a *app) removeAIKey() {
	if a.ai.saving {
		return
	}
	a.cancelAIProbe()
	provider, epoch := a.ai.draft, a.ai.epoch
	if provider.Temperature != nil {
		value := *provider.Temperature
		provider.Temperature = &value
	}
	key := aiCredentialKey(provider)
	if key == "" {
		return
	}
	a.ai.saving = true
	go func() {
		err := deleteAutoMixSecret(key)
		a.post(func() {
			a.ai.saving = false
			if err != nil && !errors.Is(err, errors.ErrUnsupported) && !errors.Is(err, os.ErrNotExist) {
				if a.ai.epoch == epoch {
					a.ai.status = T("Could not remove AI API key")
				}
				return
			}
			delete(a.ai.temporary, key)
			if a.ai.epoch == epoch {
				a.ai.keyDraft, a.ai.keyStatus = "", T("No saved key; local services may not need one")
			}
			// 旧站点的删除回调不得使新站点的运行中配置失效。
			if active, ok := a.aiProvider(a.settings.AutoMixProviderID); ok && aiCredentialKey(active) == key {
				a.configureAutoMixAdvisor()
			}
		})
	}()
}

func (a *app) deleteAIProvider() {
	provider, ok := a.aiProvider(a.ai.draft.ID)
	if !ok || !a.ai.confirmDelete || a.ai.saving {
		return
	}
	original, selected := a.settings.AIProviders, a.settings.AutoMixProviderID
	a.settings.AIProviders = slices.DeleteFunc(slices.Clone(original), func(item automix.Provider) bool { return item.ID == provider.ID })
	if selected == provider.ID {
		a.settings.AutoMixProviderID = ""
	}
	if err := a.settings.save(); err != nil {
		a.settings.AIProviders, a.settings.AutoMixProviderID = original, selected
		a.ai.status = T("Could not save provider settings")
		return
	}
	key := aiCredentialKey(provider)
	delete(a.ai.temporary, key)
	a.closeAIEditor()
	a.configureAutoMixAdvisor()
	a.ai.saving = true
	go func() {
		err := deleteAutoMixSecret(key)
		a.post(func() {
			a.ai.saving = false
			if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errors.ErrUnsupported) {
				a.ai.status = T("Provider removed; stored key could not be deleted")
			}
		})
	}()
}

func (a *app) probeAIProvider(kind string) {
	a.cancelAIProbe()
	provider, epoch := a.ai.draft, a.ai.epoch
	if provider.Temperature != nil {
		value := *provider.Temperature
		provider.Temperature = &value
	}
	if err := provider.Validate(); err != nil || kind == "test" && strings.TrimSpace(provider.Model) == "" {
		a.ai.status = T("Enter a valid address, protocol and model before testing")
		return
	}
	key := aiCredentialKey(provider)
	credential := strings.TrimSpace(a.ai.keyDraft)
	if credential == "" {
		credential = a.aiTemporary(key)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	a.ai.cancel, a.ai.job, a.ai.status = cancel, kind, ""
	if kind == "models" {
		a.ai.status = T("Fetching models…")
	} else {
		a.ai.status = T("Testing connection…")
	}
	go func() {
		defer cancel()
		value, err := readAICredential(key, credential)
		var models []automix.Model
		var elapsed time.Duration
		if err == nil {
			config := provider.Config()
			config.APIKey = value
			var advisor *automix.Advisor
			advisor, err = automix.NewAdvisor(config)
			if err == nil {
				if kind == "models" {
					models, err = advisor.Models(ctx)
				} else {
					started := time.Now()
					_, err = advisor.Plan(ctx, automix.Request{
						Schema: "ohmygo.automix.features.v1", SampleRate: automix.SampleRate,
						Candidates: []automix.Candidate{{ID: "connection-test", Start: 0, End: 48000, Ratio: 1, Profiles: []string{"original"}}},
					})
					elapsed = time.Since(started)
				}
			}
		}
		a.post(func() {
			if a.ai.epoch != epoch || !reflect.DeepEqual(a.ai.draft, provider) {
				return
			}
			a.ai.cancel, a.ai.job = nil, ""
			if err != nil {
				a.ai.status = aiProbeError(err)
				return
			}
			if kind == "models" {
				a.ai.models = models
				a.ai.status = Tf("%d models loaded; manual entry is always available", len(models))
			} else {
				a.ai.status = Tf("Connection passed · %d ms · decision format verified", elapsed.Milliseconds())
			}
		})
	}()
}

func aiProbeError(err error) string {
	var failure *automix.HTTPError
	if errors.As(err, &failure) {
		switch failure.Status {
		case 400:
			return T("Request rejected; check model, protocol and optional parameters")
		case 401, 403:
			return T("Authentication failed; check the API key and permissions")
		case 404, 405:
			return T("Endpoint not supported; check the protocol or enter the model manually")
		case 429:
			return T("Provider rate limit or quota reached; try again later")
		default:
			return Tf("Provider returned HTTP %d; local AutoMix remains available", failure.Status)
		}
	}
	return T("Request failed; check address, protocol, timeout and JSON model support")
}
