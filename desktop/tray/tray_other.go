//go:build !windows

package tray

// Tray is a no-op placeholder for non-Windows platforms.
type Tray struct{}

// Start returns nil on non-Windows platforms.
func Start(title string, onShow func(), onQuit func()) (*Tray, error) {
	return &Tray{}, nil
}

// Close is a no-op on non-Windows platforms.
func (t *Tray) Close() {
}
