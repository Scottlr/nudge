package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/review"
	"github.com/Scottlr/nudge/internal/workspace"
)

var _ app.WorkspaceRetentionStore = (*Store)(nil)
var _ app.WorkspaceRetentionCursorStore = (*Store)(nil)
var _ app.WorkspaceRetirementRepairStore = (*Store)(nil)

func (s *Store) LoadWorkspaceRetentionCursor(ctx context.Context) (app.WorkspaceRetentionCursor, error) {
	if err := s.ensureOpen(); err != nil {
		return app.WorkspaceRetentionCursor{}, err
	}
	var afterID, updated string
	err := s.db.QueryRowContext(ctx, `SELECT after_workspace_id, updated_at FROM workspace_retention_cursor WHERE id = 1`).Scan(&afterID, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return app.WorkspaceRetentionCursor{}, app.ErrWorkspaceRetentionCursorNotFound
	}
	if err != nil {
		return app.WorkspaceRetentionCursor{}, err
	}
	when, err := parseTime(updated)
	if err != nil {
		return app.WorkspaceRetentionCursor{}, app.ErrReviewStoreCorrupt
	}
	cursor := app.WorkspaceRetentionCursor{AfterID: domain.WorkspaceID(afterID), UpdatedAt: when}
	if err := cursor.Validate(); err != nil {
		return app.WorkspaceRetentionCursor{}, app.ErrReviewStoreCorrupt
	}
	return cursor, nil
}

func (s *Store) SaveWorkspaceRetentionCursor(ctx context.Context, cursor app.WorkspaceRetentionCursor) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := cursor.Validate(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_retention_cursor(id, after_workspace_id, updated_at) VALUES(1, ?, ?) ON CONFLICT(id) DO UPDATE SET after_workspace_id = excluded.after_workspace_id, updated_at = excluded.updated_at`, string(cursor.AfterID), formatTime(cursor.UpdatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListWorkspaceRetirements(ctx context.Context, phase app.WorkspaceRetirementPhase, limit uint32) ([]app.WorkspaceRetirement, error) {
	if err := s.ensureOpen(); err != nil {
		return nil, err
	}
	if err := phase.Validate(); err != nil || limit == 0 || limit > app.MaxWorkspaceRetentionCandidatePage {
		return nil, app.ErrWorkspaceRetirementConflict
	}
	rows, err := s.db.QueryContext(ctx, `SELECT retirement_json FROM workspace_retirements WHERE phase = ? ORDER BY workspace_id ASC LIMIT ?`, string(phase), int64(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	retirements := make([]app.WorkspaceRetirement, 0, limit)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var retirement app.WorkspaceRetirement
		if json.Unmarshal(data, &retirement) != nil || retirement.Validate(app.DefaultWorkspaceRetentionPolicy()) != nil || retirement.Phase != phase {
			return nil, app.ErrReviewStoreCorrupt
		}
		retirements = append(retirements, retirement)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return retirements, nil
}

type retentionWorkspaceRow struct {
	id           domain.WorkspaceID
	repositoryID domain.RepositoryID
	worktreeID   domain.WorktreeID
	sessionID    domain.ReviewSessionID
	threadID     domain.ReviewThreadID
	workspace    review.ProposalWorkspace
	updatedAt    time.Time
}

// LoadWorkspaceRetentionCandidate rereads one candidate immediately before a
// retirement effect so the stored session revision remains a real CAS fence.
func (s *Store) LoadWorkspaceRetentionCandidate(ctx context.Context, workspaceID domain.WorkspaceID) (app.WorkspaceRetentionCandidate, error) {
	if err := s.ensureOpen(); err != nil {
		return app.WorkspaceRetentionCandidate{}, err
	}
	row, err := s.loadRetentionWorkspaceRow(ctx, workspaceID)
	if err != nil {
		return app.WorkspaceRetentionCandidate{}, err
	}
	return s.retentionCandidate(ctx, row)
}

// ListWorkspaceRetentionCandidates returns a bounded keyset page and derives
// lifecycle evidence from authoritative rows without loading proposal bytes.
func (s *Store) ListWorkspaceRetentionCandidates(ctx context.Context, page app.WorkspaceRetentionPage) (app.WorkspaceRetentionPageResult, error) {
	if err := s.ensureOpen(); err != nil {
		return app.WorkspaceRetentionPageResult{}, err
	}
	if err := page.Validate(); err != nil {
		return app.WorkspaceRetentionPageResult{}, err
	}
	query := `SELECT id, repository_id, worktree_id, session_id, source_thread_id, workspace_json, updated_at
		FROM proposal_workspaces WHERE id > ? ORDER BY id ASC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, string(page.AfterID), int64(page.Limit)+1)
	if err != nil {
		return app.WorkspaceRetentionPageResult{}, err
	}
	items := make([]retentionWorkspaceRow, 0, page.Limit)
	deferred := false
	for rows.Next() {
		if uint32(len(items)) == page.Limit {
			deferred = true
			break
		}
		var row retentionWorkspaceRow
		var workspaceJSON, updated string
		if err := rows.Scan(&row.id, &row.repositoryID, &row.worktreeID, &row.sessionID, &row.threadID, &workspaceJSON, &updated); err != nil {
			_ = rows.Close()
			return app.WorkspaceRetentionPageResult{}, err
		}
		if err := json.Unmarshal([]byte(workspaceJSON), &row.workspace); err != nil || row.workspace.Validate() != nil {
			_ = rows.Close()
			return app.WorkspaceRetentionPageResult{}, app.ErrReviewStoreCorrupt
		}
		row.updatedAt, err = parseTime(updated)
		if err != nil {
			_ = rows.Close()
			return app.WorkspaceRetentionPageResult{}, app.ErrReviewStoreCorrupt
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return app.WorkspaceRetentionPageResult{}, err
	}
	if err := rows.Close(); err != nil {
		return app.WorkspaceRetentionPageResult{}, err
	}
	result := app.WorkspaceRetentionPageResult{Candidates: make([]app.WorkspaceRetentionCandidate, 0, len(items)), HasMore: deferred}
	for _, row := range items {
		candidate, err := s.retentionCandidate(ctx, row)
		if err != nil {
			return app.WorkspaceRetentionPageResult{}, err
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	if len(result.Candidates) > 0 && result.HasMore {
		result.NextID = result.Candidates[len(result.Candidates)-1].WorkspaceID
	}
	return result, nil
}

func (s *Store) loadRetentionWorkspaceRow(ctx context.Context, workspaceID domain.WorkspaceID) (retentionWorkspaceRow, error) {
	var row retentionWorkspaceRow
	var workspaceJSON, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, repository_id, worktree_id, session_id, source_thread_id, workspace_json, updated_at FROM proposal_workspaces WHERE id = ?`, workspaceID).Scan(&row.id, &row.repositoryID, &row.worktreeID, &row.sessionID, &row.threadID, &workspaceJSON, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return retentionWorkspaceRow{}, app.ErrWorkspaceRetirementNotFound
	}
	if err != nil {
		return retentionWorkspaceRow{}, err
	}
	if err := json.Unmarshal([]byte(workspaceJSON), &row.workspace); err != nil || row.workspace.Validate() != nil {
		return retentionWorkspaceRow{}, app.ErrReviewStoreCorrupt
	}
	row.updatedAt, err = parseTime(updated)
	if err != nil {
		return retentionWorkspaceRow{}, app.ErrReviewStoreCorrupt
	}
	return row, nil
}

func (s *Store) retentionCandidate(ctx context.Context, row retentionWorkspaceRow) (app.WorkspaceRetentionCandidate, error) {
	candidate := app.WorkspaceRetentionCandidate{
		RepositoryID: row.repositoryID, WorktreeID: row.worktreeID, SessionID: row.sessionID, WorkspaceID: row.id, ThreadID: row.threadID,
		ThreadResolution: review.ResolutionOpen, ProposalState: review.ProposalNone, WorkspaceState: row.workspace.State,
		ApplyTerminal: true, JournalCertain: false, OwnershipCertain: false, HistoryCertain: false,
	}
	var revision int64
	if err := s.db.QueryRowContext(ctx, "SELECT revision FROM review_sessions WHERE id = ?", row.sessionID).Scan(&revision); err != nil || revision <= 0 {
		return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
	}
	candidate.EvaluatedRevision = uint64(revision)
	var resolution, proposalState, threadUpdated string
	var latestProposal sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT resolution, proposal, latest_proposal_id, updated_at FROM review_threads WHERE id = ? AND session_id = ?`, row.threadID, row.sessionID).Scan(&resolution, &proposalState, &latestProposal, &threadUpdated)
	threadExists := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		candidate.JournalCertain = false
	} else if err != nil {
		return app.WorkspaceRetentionCandidate{}, err
	} else {
		candidate.ThreadResolution = review.ResolutionState(resolution)
		candidate.ProposalState = review.ProposalState(proposalState)
		if candidate.ThreadResolution.Validate() != nil || candidate.ProposalState.Validate() != nil {
			return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
		}
		_, parseErr := parseTime(threadUpdated)
		if parseErr != nil {
			return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
		}
		if latestProposal.Valid {
			candidate.ProposalID = domain.ProposalID(latestProposal.String)
		}
	}
	proposalExists := false
	var proposalUpdated string
	if candidate.ProposalID != "" {
		var proposalWorkspace, proposalThread, status string
		err = s.db.QueryRowContext(ctx, `SELECT workspace_id, thread_id, status, updated_at FROM proposals WHERE id = ?`, candidate.ProposalID).Scan(&proposalWorkspace, &proposalThread, &status, &proposalUpdated)
		proposalExists = err == nil
		if errors.Is(err, sql.ErrNoRows) {
			candidate.HistoryCertain = false
		} else if err != nil {
			return app.WorkspaceRetentionCandidate{}, err
		} else if proposalWorkspace != string(row.id) || proposalThread != string(row.threadID) {
			candidate.HistoryCertain = false
		} else {
			if review.ProposalStatus(status).Validate() != nil {
				return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
			}
			_, parseErr := parseTime(proposalUpdated)
			if parseErr != nil {
				return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
			}
		}
	}
	candidate.HistoryCertain = threadExists && proposalExists && candidate.ProposalID != ""
	candidate.ProposalTerminal = !retentionProposalActive(candidate.ProposalState)
	if candidate.ThreadResolution == review.ResolutionResolved && threadExists {
		when, _ := parseTime(threadUpdated)
		candidate.BasisTime = laterTime(candidate.BasisTime, when)
	}
	if candidate.ProposalTerminal && proposalExists {
		when, _ := parseTime(proposalUpdated)
		candidate.BasisTime = laterTime(candidate.BasisTime, when)
	}

	var creationJSON []byte
	err = s.db.QueryRowContext(ctx, "SELECT evidence_json FROM proposal_workspace_creation WHERE workspace_id = ?", row.id).Scan(&creationJSON)
	if err == nil {
		var evidence workspace.WorkspaceCreationEvidence
		if json.Unmarshal(creationJSON, &evidence) != nil || evidence.Validate() != nil {
			return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
		}
		if evidence.Phase == workspace.WorkspaceVerified {
			digest, digestErr := workspace.OwnershipDigest(evidence)
			if digestErr != nil {
				return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
			}
			candidate.OwnershipCertain = true
			candidate.OwnershipDigest = digest
			candidate.MarkerNonce = evidence.Nonce
			candidate.JournalCertain = true
		} else {
			candidate.RepairRequired = evidence.Phase == workspace.WorkspaceRepair
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return app.WorkspaceRetentionCandidate{}, err
	}

	var lifecyclePhase, lifecycleUpdated string
	err = s.db.QueryRowContext(ctx, `SELECT phase, updated_at FROM proposal_workspace_lifecycle WHERE workspace_id = ? ORDER BY updated_at DESC, operation_id DESC LIMIT 1`, row.id).Scan(&lifecyclePhase, &lifecycleUpdated)
	if err == nil {
		when, parseErr := parseTime(lifecycleUpdated)
		if parseErr != nil {
			return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
		}
		switch app.ProposalWorkspaceLifecyclePhase(lifecyclePhase) {
		case app.WorkspaceLifecycleReady:
			candidate.LifecycleTerminal = true
			candidate.BasisTime = laterTime(candidate.BasisTime, when)
		case app.WorkspaceLifecycleRepair:
			candidate.RepairRequired = true
			candidate.JournalCertain = false
		default:
			candidate.LifecycleTerminal = false
			candidate.JournalCertain = false
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		candidate.LifecycleTerminal = false
		candidate.JournalCertain = false
	} else {
		return app.WorkspaceRetentionCandidate{}, err
	}

	var applyID, applyPhase, applyPrepared string
	err = s.db.QueryRowContext(ctx, `SELECT id, phase, prepared_at FROM apply_operations WHERE workspace_id = ? ORDER BY prepared_at DESC, id DESC LIMIT 1`, row.id).Scan(&applyID, &applyPhase, &applyPrepared)
	if err == nil {
		candidate.ApplyOperationID = domain.OperationID(applyID)
		candidate.ApplyTerminal = retentionApplyTerminal(applyPhase)
		if applyPhase == "repair_required" {
			candidate.RepairRequired = true
			candidate.JournalCertain = false
		}
		when, parseErr := parseTime(applyPrepared)
		if parseErr != nil {
			return app.WorkspaceRetentionCandidate{}, app.ErrReviewStoreCorrupt
		}
		if candidate.ApplyTerminal {
			candidate.BasisTime = laterTime(candidate.BasisTime, when)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return app.WorkspaceRetentionCandidate{}, err
	}

	var activeTurns int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_turns WHERE thread_id = ? AND state IN ('prepared', 'started', 'steering')`, row.threadID).Scan(&activeTurns); err != nil {
		return app.WorkspaceRetentionCandidate{}, err
	}
	if activeTurns > 0 {
		candidate.ActiveLease = true
		candidate.JournalCertain = false
	}
	if row.workspace.State == review.WorkspaceRepairRequired || row.workspace.State == review.WorkspaceRemoved {
		candidate.RepairRequired = row.workspace.State == review.WorkspaceRepairRequired
		candidate.LifecycleTerminal = true
	}
	if candidate.ThreadResolution != review.ResolutionResolved && candidate.BasisTime.IsZero() {
		candidate.BasisTime = time.Time{}
	}
	if err := candidate.Validate(); err != nil {
		return app.WorkspaceRetentionCandidate{}, err
	}
	return candidate, nil
}

// LoadWorkspaceRetirement loads only the exact operation/workspace pair.
func (s *Store) LoadWorkspaceRetirement(ctx context.Context, workspaceID domain.WorkspaceID, operationID domain.OperationID) (app.WorkspaceRetirement, error) {
	if err := s.ensureOpen(); err != nil {
		return app.WorkspaceRetirement{}, err
	}
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT retirement_json FROM workspace_retirements WHERE workspace_id = ? AND operation_id = ?`, workspaceID, operationID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return app.WorkspaceRetirement{}, app.ErrWorkspaceRetirementNotFound
	}
	if err != nil {
		return app.WorkspaceRetirement{}, err
	}
	var retirement app.WorkspaceRetirement
	if json.Unmarshal(data, &retirement) != nil || retirement.Validate(app.DefaultWorkspaceRetentionPolicy()) != nil {
		return app.WorkspaceRetirement{}, app.ErrReviewStoreCorrupt
	}
	return retirement, nil
}

// SaveWorkspaceRetirement persists one immutable decision and advances only
// its allowed journal phase. It does not delete workspace or history rows.
func (s *Store) SaveWorkspaceRetirement(ctx context.Context, retirement app.WorkspaceRetirement) error {
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if retirement.Validate(app.DefaultWorkspaceRetentionPolicy()) != nil {
		return app.ErrWorkspaceRetirementConflict
	}
	data, err := json.Marshal(retirement)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existingData []byte
	var existingOperation string
	err = tx.QueryRowContext(ctx, `SELECT operation_id, retirement_json FROM workspace_retirements WHERE workspace_id = ?`, retirement.Candidate.WorkspaceID).Scan(&existingOperation, &existingData)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_retirements(operation_id, workspace_id, phase, retirement_json, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)`, retirement.OperationID, retirement.Candidate.WorkspaceID, string(retirement.Phase), data, formatTime(retirement.CreatedAt), formatTime(retirement.UpdatedAt)); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	var existing app.WorkspaceRetirement
	if json.Unmarshal(existingData, &existing) != nil || existing.Validate(app.DefaultWorkspaceRetentionPolicy()) != nil || existingOperation != string(retirement.OperationID) || !sameRetirementIdentity(existing, retirement) || !existing.Phase.CanTransitionTo(retirement.Phase) {
		return app.ErrWorkspaceRetirementConflict
	}
	if reflect.DeepEqual(existing, retirement) {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workspace_retirements SET phase = ?, retirement_json = ?, updated_at = ? WHERE workspace_id = ? AND operation_id = ?`, string(retirement.Phase), data, formatTime(retirement.UpdatedAt), retirement.Candidate.WorkspaceID, retirement.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}

func sameRetirementIdentity(left, right app.WorkspaceRetirement) bool {
	return reflect.DeepEqual(left.Candidate, right.Candidate) && reflect.DeepEqual(left.Decision, right.Decision) && left.Version == right.Version && left.OperationID == right.OperationID && left.CreatedAt.Equal(right.CreatedAt)
}

func laterTime(left, right time.Time) time.Time {
	if left.IsZero() || right.After(left) {
		return right
	}
	return left
}

func retentionProposalActive(state review.ProposalState) bool {
	return state == review.ProposalGenerating || state == review.ProposalReady || state == review.ProposalApplying
}

func retentionApplyTerminal(phase string) bool {
	return phase == "applied" || phase == "failed_clean" || phase == "repair_required"
}
