package session

type Manager struct {
	Sessions []*Session
	Active   int
}

func NewManager() *Manager { return &Manager{} }

func (m *Manager) Add(s *Session) {
	m.Sessions = append(m.Sessions, s)
	m.Active = len(m.Sessions) - 1
}

// Len returns the number of registered sessions.
func (m *Manager) Len() int {
	return len(m.Sessions)
}

func (m *Manager) Current() *Session {
	if len(m.Sessions) == 0 {
		return nil
	}
	if m.Active < 0 || m.Active >= len(m.Sessions) {
		m.Active = 0
	}
	return m.Sessions[m.Active]
}

func (m *Manager) ByID(id string) *Session {
	for _, s := range m.Sessions {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (m *Manager) Close(id string) {
	for i, s := range m.Sessions {
		if s.ID == id {
			_ = s.Close()
			m.Sessions = append(m.Sessions[:i], m.Sessions[i+1:]...)
			if len(m.Sessions) == 0 {
				m.Active = 0
				return
			}
			if m.Active >= len(m.Sessions) {
				m.Active = len(m.Sessions) - 1
			}
			return
		}
	}
}

func (m *Manager) CloseAll() {
	for _, s := range m.Sessions {
		_ = s.Close()
	}
	m.Sessions = nil
	m.Active = 0
}
