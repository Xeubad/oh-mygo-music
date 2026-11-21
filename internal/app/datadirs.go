package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
)

// appName is the app's name, as mygo.config.ts has it. The folder the app
// keeps the listener's data and cache in is named after it.
const appName = "MyGo Music by XEU"

// legacyAppName is the name those folders had before, and still have.
const legacyAppName = "MyGo Music"

// keepOldDataDirs points the app's folders at those of the versions named
// MyGo Music, with the name they had, MyGo Music Dev included. Without it
// the app would open without the listener's settings and session, the local
// library, the plugins' data and the cached songs, as on first use.
func keepOldDataDirs() {
	name, ok := legacyName(mygo.App.Name())
	if !ok {
		return
	}
	if dir, err := os.UserConfigDir(); err == nil {
		keepPath(mygo.PathUserData, filepath.Join(dir, name))
	}
	if dir, err := os.UserCacheDir(); err == nil {
		keepPath(mygo.PathCache, filepath.Join(dir, name))
	}
}

// keepPath points a folder at dir, which it makes: Path makes a folder named
// after the app, and SetPath returns one without making it, though some files
// are written without making theirs first, as the session is.
func keepPath(name mygo.PathName, dir string) {
	if os.MkdirAll(dir, 0o755) == nil {
		mygo.App.SetPath(name, dir)
	}
}

// legacyName returns the name of the existing data folder for this branded
// app (including its development build).
func legacyName(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, appName)
	if !ok {
		return "", false
	}
	return legacyAppName + rest, true
}

// legacyLockName preserves MyGo's existing single-instance socket name across
// the product rename, so an old or new launch still finds the running app.
func legacyLockName(name string) (string, bool) {
	return legacyName(name)
}
