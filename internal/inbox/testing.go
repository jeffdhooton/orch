package inbox

// SetDirForTest overrides the inbox directory. Call the returned function to restore.
// This is intended for use in tests only.
func SetDirForTest(dir string) func() {
	prev := inboxDirOverride
	inboxDirOverride = dir
	return func() { inboxDirOverride = prev }
}
