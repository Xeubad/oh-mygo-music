package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

func (a *app) onlineShareSettings(c *ui.Context, p palette, row settingsRow, block settingsBlock) {
	row(T("Share Service Address"), T("Your self-hosted HTTPS address. Local HTTP is allowed for localhost."), func() {
		ui.Column(c).Width(320).MaxWidthPercent(100).Gap(6).Children(func() {
			ui.TextInput(c.Key("share-server-url"), &a.settings.ShareServerURL).
				Label(T("Share Service Address")).Placeholder("https://share.example.com").FontSize(13)
			if ui.Button(c.Key("save-share-server"), T("Save Address")).Clicked() {
				normalized, err := normalizeShareServerURL(a.settings.ShareServerURL)
				if err != nil {
					a.shareSettingsStatus = T("Enter a valid HTTPS share address")
					return
				}
				a.settings.ShareServerURL = normalized
				a.changed()
				a.shareSettingsStatus = T("Share address saved")
			}
		})
	})
	row(T("Share Service Token"), T("Stored in the operating system credential store, not settings.json."), func() {
		ui.Column(c).Width(320).MaxWidthPercent(100).Gap(6).Children(func() {
			ui.TextInput(c.Key("share-service-token"), &a.shareTokenDraft).
				Label(T("Share Service Token")).Password().FontSize(13).Disabled(a.shareTokenSaving)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c.Key("save-share-token"), T("Save Token Securely")).
					Disabled(a.shareTokenSaving || strings.TrimSpace(a.shareTokenDraft) == "" ||
						strings.TrimSpace(a.settings.ShareServerURL) == "").
					Clicked() {
					a.saveShareServiceToken()
				}
				if ui.Button(c.Key("remove-share-token"), T("Remove Saved Token")).
					Disabled(a.shareTokenSaving || strings.TrimSpace(a.settings.ShareServerURL) == "").
					Clicked() {
					a.removeShareServiceToken()
				}
			})
			if a.shareSettingsStatus != "" {
				ui.Text(c, a.shareSettingsStatus).FontSize(11.5).TextColor(p.muted).LineHeight(1.4)
			}
		})
	})
	block(T("My Shares"), func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Row(c).MinHeight(36).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, T("Shares on the configured service")).FontSize(12.5).TextColor(p.muted)
				ui.Spacer(c)
				if ui.Button(c.Key("refresh-shares"), T("Refresh")).Disabled(a.shareListLoading).Clicked() {
					a.loadShareList()
				}
			})
			if a.shareListLoading {
				ui.Text(c, T("Loading shares…")).FontSize(12).TextColor(p.muted)
			}
			if a.shareListError != "" {
				ui.Text(c, a.shareListError).FontSize(12).TextColor(p.muted)
			}
			if !a.shareListLoading && a.shareListError == "" && len(a.shareList) == 0 {
				ui.Text(c, T("No online shares yet")).FontSize(12).TextColor(p.muted)
			}
			for _, share := range a.shareList {
				item := share
				ui.Row(c.Key(item.ID)).MinHeight(42).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, item.Title).FontSize(13).FontWeight(500).SingleLine()
						detail := item.Artists
						if detail != "" {
							detail += " · "
						}
						detail += shareStatusLabel(item.Status)
						ui.Text(c, detail).FontSize(11.5).TextColor(p.muted).SingleLine()
					})
					if item.URL != "" && ui.Button(c.Key("copy"), T("Copy Link")).Clicked() {
						if err := copyText(item.URL); err != nil {
							a.toast = T("Could not copy link")
						} else {
							a.toast = T("Link copied")
						}
					}
					if item.Status != "revoked" && item.Status != "expired" && ui.Button(c.Key("revoke"), T("Revoke")).Clicked() {
						a.revokeShare(item.ID)
					}
				})
			}
		})
	})
}

func (a *app) saveShareServiceToken() {
	serverURL, token := a.settings.ShareServerURL, strings.TrimSpace(a.shareTokenDraft)
	key := shareTokenKey(serverURL)
	if key == "" || token == "" {
		a.shareSettingsStatus = T("Enter a valid share address and token")
		return
	}
	a.shareTokenSaving = true
	a.shareSettingsStatus = T("Saving token…")
	go func() {
		err := saveShareServiceToken(serverURL, token)
		a.post(func() {
			a.shareTokenSaving = false
			if err == nil {
				a.shareTokenTemp, a.shareTokenTempKey = "", ""
				a.shareTokenDraft = ""
				a.shareSettingsStatus = T("Token saved securely")
				return
			}
			if errors.Is(err, errors.ErrUnsupported) {
				a.shareTokenTemp, a.shareTokenTempKey = token, key
				a.shareTokenDraft = ""
				a.shareSettingsStatus = T("Credential store unavailable; token is kept for this session only")
				return
			}
			a.shareSettingsStatus = Tf("Could not save token: %s", err)
		})
	}()
}

func (a *app) removeShareServiceToken() {
	key := shareTokenKey(a.settings.ShareServerURL)
	if key == "" {
		a.shareSettingsStatus = T("Enter a valid share address")
		return
	}
	a.shareTokenSaving = true
	go func() {
		err := deleteShareServiceSecret(key)
		a.post(func() {
			a.shareTokenSaving = false
			if err != nil {
				a.shareSettingsStatus = Tf("Could not remove token: %s", err)
				return
			}
			if a.shareTokenTempKey == key {
				a.shareTokenTemp, a.shareTokenTempKey = "", ""
			}
			a.shareSettingsStatus = T("Saved token removed")
		})
	}()
}

func (a *app) loadShareList() {
	if a.shareListLoading {
		return
	}
	a.shareListLoading, a.shareListError = true, ""
	serverURL := a.settings.ShareServerURL
	client, clientErr := a.shareClientForSettings()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := clientErr
		var shares []shareListItem
		if err == nil {
			shares, err = client.list(ctx)
		}
		a.post(func() {
			a.shareListLoading = false
			if a.settings.ShareServerURL != serverURL {
				return
			}
			if err != nil {
				a.shareListError = T("Could not load shares") + ": " + err.Error()
				return
			}
			if normalize, normalizeErr := normalizeShareServerURL(serverURL); normalizeErr != nil || normalize != a.settings.ShareServerURL {
				return
			}
			a.shareList = shares
		})
	}()
}

func (a *app) revokeShare(id string) {
	if id == "" {
		return
	}
	client, clientErr := a.shareClientForSettings()
	serverURL := a.settings.ShareServerURL
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		err := clientErr
		if err == nil {
			err = client.revoke(ctx, id)
		}
		a.post(func() {
			if a.settings.ShareServerURL != serverURL {
				return
			}
			if err != nil {
				a.shareListError = T("Could not revoke share") + ": " + err.Error()
				return
			}
			a.loadShareList()
		})
	}()
}

func shareStatusLabel(status string) string {
	switch status {
	case "awaiting_upload":
		return T("Waiting for audio")
	case "uploading":
		return T("Uploading")
	case "processing":
		return T("Preparing")
	case "ready":
		return T("Available")
	case "failed":
		return T("Failed")
	case "expired":
		return T("Expired")
	case "revoked":
		return T("Revoked")
	default:
		return status
	}
}
