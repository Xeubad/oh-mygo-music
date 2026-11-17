package app

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
)

// 系统写入边界可替换，自动验收不接触用户的剪贴板。
var copyImage = func(data []byte) error {
	return mygo.Clipboard.Write(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, data))))
}

var saveCardAs = func(a *app, name, directory string) (string, error) {
	if directory == "" {
		directory, _ = mygo.App.Path(mygo.PathDownloads)
	}
	return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: a.win, Title: T("Save PNG"), DefaultPath: filepath.Join(directory, name),
		Filters: []mygo.FileFilter{{Name: "PNG", Extensions: []string{"png"}}}, CreateDirectories: true})
}

type shareOutput uint8

const (
	shareOutputNone shareOutput = iota
	shareOutputCopyImage
	shareOutputSaveImage
	shareOutputCopyLink
)

func (a *app) prepareOnlineCardOutput(draft *shareDraft, output shareOutput) bool {
	if !draft.cardOnline && output != shareOutputCopyLink {
		return false
	}
	if draft.onlinePublishing || draft.exporting || draft.pendingOutput != shareOutputNone {
		return true
	}
	// 每次导出重新确认服务端有效期与撤销状态，不能仅凭本地 URL 判断可以复用。
	draft.pendingOutput, draft.pendingOutputRevision = output, draft.onlineRevision
	a.publishOnlineShare(draft)
	if !draft.onlinePublishing {
		draft.pendingOutput = shareOutputNone
	}
	return true
}

func (a *app) resumeShareOutput(draft *shareDraft) {
	if a.sharing != draft || !draft.open || draft.pendingOutput == shareOutputNone {
		return
	}
	if draft.pendingOutputRevision != draft.onlineRevision {
		draft.pendingOutput = shareOutputNone
		return
	}
	if draft.onlinePublishing || !a.onlineLinkMatchesDraft(draft) {
		return
	}
	output := draft.pendingOutput
	if output != shareOutputCopyLink && (len(draft.png) == 0 || draft.pngURL != draft.onlineURL) {
		return
	}
	// 发布和卡片重绘都完成后再执行原来的导出动作，不能拿上传前的旧 PNG。
	draft.pendingOutput = shareOutputNone
	switch output {
	case shareOutputCopyImage:
		a.copyPreparedShareImage(draft)
	case shareOutputSaveImage:
		a.savePreparedShareImage(draft)
	case shareOutputCopyLink:
		a.copyPublishedShareLink(draft)
	}
}

func (a *app) saveShareImage(draft *shareDraft) {
	if a.prepareOnlineCardOutput(draft, shareOutputSaveImage) {
		return
	}
	a.savePreparedShareImage(draft)
}

func (a *app) savePreparedShareImage(draft *shareDraft) {
	if len(draft.png) == 0 || draft.exporting {
		return
	}
	data, name, directory := slices.Clone(draft.png), songFileName(&draft.track, "png"), a.settings.CardDirectory
	pick := saveCardAs
	draft.exporting, draft.status = true, ""
	go func() {
		path, err := pick(a, name, directory)
		if err == nil && path != "" {
			err = writeCardPNG(path, data)
		}
		a.post(func() {
			if err == nil && path != "" {
				a.settings.CardDirectory = filepath.Dir(path)
				if saveErr := a.settings.save(); saveErr != nil {
					a.toast = T("Could not save display preferences")
				}
			}
			if a.sharing != draft || !draft.open {
				return
			}
			draft.exporting = false
			switch {
			case err != nil:
				draft.status = T("Could not save image")
			case path == "":
				draft.status = T("Save canceled")
			default:
				draft.status = T("Image saved")
			}
		})
	}()
}

func writeCardPNG(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".mygo-card-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	// 完整编码并落盘后才替换目标，失败不会截断用户已有图片。
	return os.Rename(file.Name(), path)
}

func (a *app) copyShareImage(draft *shareDraft) {
	if a.prepareOnlineCardOutput(draft, shareOutputCopyImage) {
		return
	}
	a.copyPreparedShareImage(draft)
}

func (a *app) copyPreparedShareImage(draft *shareDraft) {
	if len(draft.png) == 0 || draft.exporting {
		return
	}
	data := slices.Clone(draft.png)
	draft.exporting, draft.status = true, ""
	copy := copyImage
	go func() {
		err := copy(data)
		a.post(func() {
			if a.sharing != draft || !draft.open {
				return
			}
			draft.exporting = false
			draft.status = T("Image copied")
			if err != nil {
				draft.status = T("Could not copy image")
			}
		})
	}()
}
