package tmux

import "sync"

// Mock is a test double for tmux.Runner that records calls instead of
// executing real tmux commands.
type Mock struct {
	mu       sync.Mutex
	Sessions map[string]bool
	Windows  map[string][]string // session -> window names
	SentKeys []SentKey
	Killed   []string // "session:window" entries
}

// SentKey records a SendKeys call.
type SentKey struct {
	Session, Window, Text string
}

// NewMock creates a ready-to-use Mock.
func NewMock() *Mock {
	return &Mock{
		Sessions: make(map[string]bool),
		Windows:  make(map[string][]string),
	}
}

func (m *Mock) HasSession(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Sessions[name]
}

func (m *Mock) NewSession(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sessions[name] = true
	return nil
}

func (m *Mock) NewWindow(session, name, dir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sessions[session] = true
	m.Windows[session] = append(m.Windows[session], name)
	return nil
}

func (m *Mock) KillWindow(session, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Killed = append(m.Killed, session+":"+name)
	// Remove from window list.
	wins := m.Windows[session]
	for i, w := range wins {
		if w == name {
			m.Windows[session] = append(wins[:i], wins[i+1:]...)
			break
		}
	}
	return nil
}

func (m *Mock) SendKeys(session, window, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SentKeys = append(m.SentKeys, SentKey{session, window, text})
	return nil
}

func (m *Mock) CapturePane(session, window string, lines int) (string, error) {
	return "mock pane output\n", nil
}

func (m *Mock) SelectWindow(session, name string) error {
	return nil
}

func (m *Mock) KillSession(session string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Sessions, session)
	delete(m.Windows, session)
	return nil
}

func (m *Mock) ListWindows(session string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Windows[session], nil
}
