//go:build !unix

package backup

// LockDataDir is a no-op on non-Unix systems (development only; the
// supported deployment target is Linux).
func LockDataDir(dir string) (func(), error) { return func() {}, nil }
