package threads

// Update applies one typed frontend message and emits inert root intents.
func (m *Model) Update(message any) []Intent {
	if m == nil {
		return nil
	}
	switch value := message.(type) {
	case SetSnapshotMsg:
		return m.replaceSnapshot(value.Snapshot)
	case PageResultMsg:
		m.acceptPage(value.Result)
	case PageErrorMsg:
		if m.pending != nil && value.Request.Token == m.pending.Token && value.Request.Revision == m.snapshotRevision {
			m.pending = nil
			if value.Err != nil {
				m.lastError = "thread page request failed"
			}
		}
	case MoveSelectionMsg:
		m.moveSelection(value.Delta)
	case ActivateSelectionMsg:
		if m.selected != "" && m.sessionID != "" {
			return []Intent{{Activate: &ActivateIntent{SessionID: m.sessionID, ThreadID: m.selected}}}
		}
	case LoadNextPageMsg:
		if request := m.pageRequest(m.nextCursor); request != nil {
			return []Intent{{PageRequest: request}}
		}
	case SetFocusMsg:
		m.focused = value.Focused
	}
	return nil
}
