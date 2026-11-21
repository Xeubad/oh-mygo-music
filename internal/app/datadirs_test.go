package app

import "testing"

// TestLegacyName checks that an app built as MyGo Music by XEU, and its
// development build, keep the folders of the versions named MyGo Music.
func TestLegacyName(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		ok         bool
	}{
		{"MyGo Music by XEU", "MyGo Music", true},
		{"MyGo Music by XEU Dev", "MyGo Music Dev", true},
		{"mygo-music", "", false},
		{"MyGo Music", "", false},
	} {
		got, ok := legacyName(tt.name)
		if got != tt.want || ok != tt.ok {
			t.Errorf("legacyName(%q) = %q, %v, want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// TestLegacyLockName keeps the old process lock name while the UI uses the new brand.
func TestLegacyLockName(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		ok         bool
	}{
		{"MyGo Music by XEU", "MyGo Music", true},
		{"MyGo Music by XEU Dev", "MyGo Music Dev", true},
		{"MyGo Music", "", false},
	} {
		got, ok := legacyLockName(tc.name)
		if got != tc.want || ok != tc.ok {
			t.Errorf("legacyLockName(%q) = %q, %v, want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
