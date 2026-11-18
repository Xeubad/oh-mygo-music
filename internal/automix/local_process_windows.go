package automix

import (
	"os/exec"
	"strconv"
	"syscall"
)

func quietEngineCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		// 取消时连同 Rubber Band 子进程一起退出，不能只杀 Python 留下后台作业。
		stop := exec.Command("taskkill.exe", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F")
		stop.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		return stop.Run()
	}
}
