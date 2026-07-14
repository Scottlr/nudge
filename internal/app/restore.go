package app

import "context"

// RestoreSession performs the default writable restore choice explicitly. A
// caller that needs immutable history or a distinct session must use
// OpenSession with SessionReadOnly or SessionDistinct instead.
func (m *SessionManager) RestoreSession(ctx context.Context, request OpenSessionRequest) (SessionHandle, error) {
	request.Mode = SessionWritable
	return m.OpenSession(ctx, request)
}
