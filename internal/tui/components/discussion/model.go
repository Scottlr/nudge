package discussion

import (
	"strings"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/theme"
	"github.com/Scottlr/nudge/internal/tui/components/comment"
	"github.com/Scottlr/nudge/internal/tui/viewport"
)

const (
	maxRetainedMessages = 200
	maxBodyRequests     = 16
	defaultOverscan     = 2
	defaultHeight       = 12
)

type bodyState struct {
	chunk app.MessageBodyChunk
	err   string
	ready bool
}

// Model is the disposable discussion state for one active thread.
type Model struct {
	thread        *app.ThreadSummary
	revision      uint64
	messages      []app.MessageSummary
	nextCursor    *app.MessageCursor
	pendingPage   *MessagePageRequest
	pendingBodies map[domain.MessageID]BodyRangeRequest
	bodies        map[domain.MessageID]bodyState
	nextToken     uint64
	selected      domain.MessageID
	top           int
	width         int
	height        int
	overscan      int
	focused       bool
	replyFocused  bool
	draft         *comment.Model
	budget        viewport.RenderBudget
	theme         theme.Theme
	lastError     string
}

// NewModel creates an empty bounded discussion projection.
func NewModel() *Model {
	return &Model{
		overscan:      defaultOverscan,
		pendingBodies: make(map[domain.MessageID]BodyRangeRequest),
		bodies:        make(map[domain.MessageID]bodyState),
		budget:        viewport.DefaultRenderBudget(),
		theme:         theme.BuiltinTerminalDefault(),
	}
}

// SetSize updates the terminal-cell discussion viewport.
func (m *Model) SetSize(width, height int) {
	if m == nil {
		return
	}
	m.width, m.height = maxInt(width, 0), maxInt(height, 0)
	m.reposition()
}

// SetTheme supplies a resolved semantic theme.
func (m *Model) SetTheme(value theme.Theme) {
	if m != nil && value.Validate() == nil {
		m.theme = value
	}
}

// SetBudget supplies the shared T073 frame budget.
func (m *Model) SetBudget(value viewport.RenderBudget) {
	if m != nil && value.Validate() == nil {
		m.budget = value
	}
}

// Selected returns the stable selected message identity.
func (m *Model) Selected() domain.MessageID {
	if m == nil {
		return ""
	}
	return m.selected
}

// Thread returns a defensive copy of the active summary.
func (m *Model) Thread() *app.ThreadSummary {
	if m == nil || m.thread == nil {
		return nil
	}
	copyValue := *m.thread
	return &copyValue
}

// InitialPageRequest asks the root for the first message metadata page.
func (m *Model) InitialPageRequest() []Intent {
	if m == nil || m.thread == nil || m.revision == 0 || len(m.messages) != 0 || m.pendingPage != nil {
		return nil
	}
	m.nextToken++
	request := MessagePageRequest{ThreadID: m.thread.ID, Revision: m.revision, Limit: defaultPageLimit, Token: m.nextToken}
	m.pendingPage = &request
	return []Intent{{MessagePageRequest: &request}}
}

func (m *Model) setThread(message SetThreadMsg) []Intent {
	if message.Revision < m.revision {
		return nil
	}
	var nextThread *app.ThreadSummary
	if message.Thread != nil && message.Thread.ID != "" {
		copyValue := *message.Thread
		nextThread = &copyValue
	}
	previousID := domain.ReviewThreadID("")
	if m.thread != nil {
		previousID = m.thread.ID
	}
	nextID := domain.ReviewThreadID("")
	if nextThread != nil {
		nextID = nextThread.ID
	}
	if previousID != nextID || message.Revision != m.revision {
		m.messages = nil
		m.nextCursor = nil
		m.pendingPage = nil
		m.pendingBodies = make(map[domain.MessageID]BodyRangeRequest)
		m.bodies = make(map[domain.MessageID]bodyState)
		m.selected = ""
	}
	m.thread = nextThread
	m.revision = message.Revision
	if m.thread == nil {
		m.replyFocused = false
		m.draft = nil
		return nil
	}
	if m.draft == nil || previousID != nextID {
		m.draft = comment.NewModel(review.CodeAnchor{})
	}
	if m.selected != "" && !m.containsMessage(m.selected) {
		m.selected = ""
	}
	m.reposition()
	return m.InitialPageRequest()
}

func (m *Model) pageRequest() *MessagePageRequest {
	if m.thread == nil || m.pendingPage != nil || m.nextCursor == nil {
		return nil
	}
	m.nextToken++
	request := MessagePageRequest{ThreadID: m.thread.ID, Revision: m.revision, Cursor: cloneMessageCursor(m.nextCursor), Limit: defaultPageLimit, Token: m.nextToken}
	m.pendingPage = &request
	return &request
}

func (m *Model) acceptPage(result MessagePageResult) []Intent {
	if m.pendingPage == nil || result.Request.Token == 0 || result.Request.Token != m.pendingPage.Token || result.Request.ThreadID != m.threadID() || result.Request.Revision != m.revision || !sameMessageCursor(result.Request.Cursor, m.pendingPage.Cursor) || result.Page.Revision != result.Request.Revision {
		return nil
	}
	m.pendingPage = nil
	for _, message := range result.Page.Items {
		if message.ID == "" || message.ThreadID != m.threadID() {
			m.lastError = "invalid message page"
			return nil
		}
	}
	if result.Request.Cursor == nil {
		m.messages = cloneMessages(result.Page.Items)
	} else {
		m.messages = mergeMessages(m.messages, result.Page.Items)
	}
	if len(m.messages) > maxRetainedMessages {
		m.messages = m.messages[:maxRetainedMessages]
	}
	m.nextCursor = cloneMessageCursor(result.Page.Next)
	if result.Page.HasMore && m.nextCursor == nil {
		m.lastError = "invalid message page cursor"
		return nil
	}
	if m.selected == "" && len(m.messages) > 0 {
		m.selected = m.messages[0].ID
	}
	m.reposition()
	intents := make([]Intent, 0, maxBodyRequests)
	for _, message := range m.messages {
		if len(intents) >= maxBodyRequests {
			break
		}
		if request := m.bodyRequest(message); request != nil {
			intents = append(intents, Intent{BodyRangeRequest: request})
		}
	}
	return intents
}

func (m *Model) bodyRequest(message app.MessageSummary) *BodyRangeRequest {
	if message.ByteLength == 0 || message.SHA256 == "" || m.pendingBodies[message.ID].Token != 0 {
		return nil
	}
	if state, ok := m.bodies[message.ID]; ok && state.ready {
		return nil
	}
	length := message.ByteLength
	if length > app.MaxMessageBodyRange {
		length = app.MaxMessageBodyRange
	}
	rangeValue := app.BodyRange{MessageID: message.ID, ExpectedLength: message.ByteLength, ExpectedSHA256: message.SHA256, Length: length}
	if rangeValue.Validate() != nil {
		return nil
	}
	m.nextToken++
	request := BodyRangeRequest{ThreadID: message.ThreadID, Revision: m.revision, Range: rangeValue, Token: m.nextToken}
	m.pendingBodies[message.ID] = request
	return &request
}

func (m *Model) acceptBody(result BodyRangeResult, requestError error) {
	request, ok := m.pendingBodies[result.Request.Range.MessageID]
	if !ok || request.Token != result.Request.Token || result.Request.ThreadID != m.threadID() || result.Request.Revision != m.revision || result.Request.Range != request.Range {
		return
	}
	delete(m.pendingBodies, result.Request.Range.MessageID)
	if requestError != nil {
		m.bodies[result.Request.Range.MessageID] = bodyState{err: "message body unavailable"}
		return
	}
	chunk := result.Chunk
	if chunk.MessageID != request.Range.MessageID || chunk.Offset != request.Range.Offset || chunk.TotalLength != request.Range.ExpectedLength || chunk.SHA256 != request.Range.ExpectedSHA256 || uint64(len(chunk.Bytes)) > request.Range.Length || chunk.Complete && uint64(len(chunk.Bytes)) != request.Range.Length {
		m.bodies[result.Request.Range.MessageID] = bodyState{err: "message body identity unavailable"}
		return
	}
	m.bodies[result.Request.Range.MessageID] = bodyState{chunk: cloneChunk(chunk), ready: chunk.Complete}
}

func (m *Model) presentMessage(id domain.MessageID) []Intent {
	if m.thread == nil || m.thread.Read != review.Unread || id == "" {
		return nil
	}
	state, ok := m.bodies[id]
	if !ok || !state.ready {
		return nil
	}
	if !m.containsMessage(id) {
		return nil
	}
	return []Intent{{MarkRead: &MarkReadIntent{ThreadID: m.thread.ID}}}
}

func (m *Model) sendReply() []Intent {
	if m.thread == nil || m.draft == nil || !m.draft.CanSubmit() {
		return nil
	}
	return []Intent{{Reply: &ReplyIntent{ThreadID: m.thread.ID, Text: trimBlankLines(m.draft.Value())}}}
}

func (m *Model) containsMessage(id domain.MessageID) bool {
	for _, message := range m.messages {
		if message.ID == id {
			return true
		}
	}
	return false
}

func (m *Model) threadID() domain.ReviewThreadID {
	if m.thread == nil {
		return ""
	}
	return m.thread.ID
}

func (m *Model) selectedIndex() int {
	for index, message := range m.messages {
		if message.ID == m.selected {
			return index
		}
	}
	return 0
}

func (m *Model) reposition() {
	m.top = viewport.Window(len(m.messages), m.selectedIndex(), m.top, m.renderHeight(), m.overscan).Top
}

func (m *Model) renderHeight() int {
	if m.height <= 0 {
		return defaultHeight
	}
	return m.height
}

func (m *Model) moveSelection(delta int) {
	if delta == 0 || len(m.messages) == 0 {
		return
	}
	index := clampInt(m.selectedIndex()+delta, 0, len(m.messages)-1)
	m.selected = m.messages[index].ID
	m.reposition()
}

func cloneMessages(values []app.MessageSummary) []app.MessageSummary {
	if len(values) == 0 {
		return nil
	}
	result := make([]app.MessageSummary, len(values))
	copy(result, values)
	return result
}

func mergeMessages(existing, incoming []app.MessageSummary) []app.MessageSummary {
	result := cloneMessages(existing)
	seen := make(map[domain.MessageID]struct{}, len(result))
	for _, message := range result {
		seen[message.ID] = struct{}{}
	}
	for _, message := range incoming {
		if _, ok := seen[message.ID]; ok {
			continue
		}
		seen[message.ID] = struct{}{}
		result = append(result, message)
	}
	return result
}

func cloneChunk(value app.MessageBodyChunk) app.MessageBodyChunk {
	value.Bytes = append([]byte(nil), value.Bytes...)
	return value
}

func trimBlankLines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	first, last := 0, len(lines)-1
	for first <= last && strings.TrimSpace(lines[first]) == "" {
		first++
	}
	for last >= first && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if first > last {
		return ""
	}
	return strings.Join(lines[first:last+1], "\n")
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
