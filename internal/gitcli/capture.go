package gitcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Scottlr/nudge/internal/app"
	"github.com/Scottlr/nudge/internal/domain"
	"github.com/Scottlr/nudge/internal/domain/repository"
	"github.com/Scottlr/nudge/internal/process"
)

const (
	// ErrorLocalCaptureLimit reports a bounded capture beyond a T070 limit.
	ErrorLocalCaptureLimit ErrorCode = "local_capture_limit"
	// ErrorLocalCaptureMutation reports evidence changing during capture.
	ErrorLocalCaptureMutation ErrorCode = "local_capture_mutation"
	// ErrorLocalCaptureIncomplete reports evidence that cannot safely form a candidate.
	ErrorLocalCaptureIncomplete ErrorCode = "local_capture_incomplete"
)

// LocalCaptureConfig supplies the trusted Git/process boundary and the two
// application-owned heavy-artifact ports required by T106.
type LocalCaptureConfig struct {
	Executable       process.ExecutableIdentity
	Runner           process.Runner
	IDs              app.IDSource
	Policy           app.ResourcePolicy
	MachinePolicy    MachineGitReadPolicyV1
	RenamePolicy     RenamePolicyV1
	PatchPolicy      PatchFormatV1
	ConversionPolicy ContentConversionPolicyV1
	Capacity         app.CapacityReservationPort
	Spools           app.ArtifactSpoolPort
	OperationID      domain.OperationID
	VolumeID         string
	VolumeEvidence   []app.VolumeEvidence
	Budget           app.CapacityBudget
}

// LocalCaptureResult retains the typed owner handles needed by T009. The
// reservation remains live until the consumer adopts or aborts the result.
type LocalCaptureResult struct {
	Candidate   repository.LocalCaptureCandidate
	PatchSpool  app.ArtifactSpoolHandle
	BlobSpool   app.ArtifactSpoolHandle
	Reservation app.CapacityReservation
	Plan        app.CapacityPlan
	Policy      app.ResourcePolicy
	capacity    app.CapacityReservationPort
	policy      app.ResourcePolicy
}

// Capacity returns the reservation owner used by this capture. It lets the
// application transfer the typed release capability during T009 adoption
// without exposing adapter internals or path state.
func (r *LocalCaptureResult) Capacity() app.CapacityReservationPort {
	if r == nil {
		return nil
	}
	return r.capacity
}

// Abort releases candidate-owned spools and the matching capacity marker. It
// never touches the user worktree, index, refs, or Git metadata.
func (r *LocalCaptureResult) Abort(ctx context.Context) error {
	if r == nil || ctx == nil {
		return &GitError{Code: ErrorInvalidInput}
	}
	var first error
	if r.PatchSpool != nil {
		if err := r.PatchSpool.Abort(ctx); err != nil && first == nil {
			first = err
		}
	}
	if r.BlobSpool != nil {
		if err := r.BlobSpool.Abort(ctx); err != nil && first == nil {
			first = err
		}
	}
	if r.capacity != nil {
		if err := r.capacity.Release(ctx, r.Reservation, r.Plan, r.policy); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// LocalCaptureAdapter computes one complete immutable local candidate. It is
// deliberately a Git-facing adapter and owns no target generation state.
type LocalCaptureAdapter struct {
	executable       process.ExecutableIdentity
	runner           process.Runner
	policy           app.ResourcePolicy
	machinePolicy    MachineGitReadPolicyV1
	renamePolicy     RenamePolicyV1
	patchPolicy      PatchFormatV1
	conversionPolicy ContentConversionPolicyV1
	capacity         app.CapacityReservationPort
	spools           app.ArtifactSpoolPort
	operationID      domain.OperationID
	volumeID         string
	volumeEvidence   []app.VolumeEvidence
	budget           app.CapacityBudget
}

// NewLocalCaptureAdapter validates the complete T065/T066/T069 dependency
// set before any repository read or filesystem mutation occurs.
func NewLocalCaptureAdapter(config LocalCaptureConfig) (*LocalCaptureAdapter, error) {
	if err := config.Executable.Validate(); err != nil || config.Capacity == nil || config.Spools == nil {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	if config.Runner == nil {
		config.Runner = process.NewRunner()
	}
	if config.IDs == nil {
		config.IDs = app.RandomIDSource{}
	}
	if config.Policy == (app.ResourcePolicy{}) {
		config.Policy = app.DefaultResourcePolicy()
	}
	if err := config.Policy.Validate(); err != nil {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	if config.MachinePolicy == (MachineGitReadPolicyV1{}) {
		config.MachinePolicy = DefaultMachineGitReadPolicyV1()
	}
	if config.MachinePolicy.StdoutLimit > int64(config.Policy.Process.MaxOutputBytes) {
		config.MachinePolicy.StdoutLimit = int64(config.Policy.Process.MaxOutputBytes)
	}
	if config.MachinePolicy.StderrLimit > int64(config.Policy.Process.MaxStderrBytes) {
		config.MachinePolicy.StderrLimit = int64(config.Policy.Process.MaxStderrBytes)
	}
	if err := config.MachinePolicy.validate(); err != nil {
		return nil, err
	}
	if config.RenamePolicy == (RenamePolicyV1{}) {
		config.RenamePolicy = DefaultRenamePolicyV1()
	}
	if err := config.RenamePolicy.Validate(); err != nil {
		return nil, err
	}
	if config.PatchPolicy == (PatchFormatV1{}) {
		config.PatchPolicy = DefaultPatchFormatV1()
	}
	if err := config.PatchPolicy.Validate(); err != nil {
		return nil, err
	}
	if config.ConversionPolicy == (ContentConversionPolicyV1{}) {
		config.ConversionPolicy = DefaultContentConversionPolicyV1()
	}
	if err := config.ConversionPolicy.Validate(); err != nil {
		return nil, err
	}
	operationID := config.OperationID
	if operationID == "" {
		value, err := domain.NewOperationID(config.IDs.NewID())
		if err != nil {
			return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
		}
		operationID = value
	}
	if config.VolumeID == "" && len(config.VolumeEvidence) == 1 {
		config.VolumeID = config.VolumeEvidence[0].ID
	}
	if config.VolumeID == "" {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: errors.New("capture volume")}
	}
	return &LocalCaptureAdapter{
		executable:       config.Executable,
		runner:           config.Runner,
		policy:           config.Policy,
		machinePolicy:    config.MachinePolicy,
		renamePolicy:     config.RenamePolicy,
		patchPolicy:      config.PatchPolicy,
		conversionPolicy: config.ConversionPolicy,
		capacity:         config.Capacity,
		spools:           config.Spools,
		operationID:      operationID,
		volumeID:         config.VolumeID,
		volumeEvidence:   append([]app.VolumeEvidence(nil), config.VolumeEvidence...),
		budget:           config.Budget,
	}, nil
}

// Capture computes one complete candidate from the supplied resolved
// repository/worktree pair. A non-nil result owns all artifacts until T009
// adopts or Abort is called.
func (a *LocalCaptureAdapter) Capture(ctx context.Context, repo repository.Repository, worktree repository.WorktreeRef) (result *LocalCaptureResult, err error) {
	if a == nil || ctx == nil {
		return nil, &GitError{Code: ErrorInvalidInput}
	}
	if err := repo.Validate(); err != nil {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	if err := worktree.Validate(); err != nil || worktree.RepositoryID != repo.ID {
		return nil, &GitError{Code: ErrorInvalidInput, Cause: err}
	}
	builder, err := NewCommandBuilder(CommandBuilderConfig{
		Executable: a.executable,
		Runner:     a.runner,
		StartPath:  worktree.RootPath,
		Policy:     a.machinePolicy,
	})
	if err != nil {
		return nil, err
	}
	base, headToken, err := a.captureBase(ctx, builder)
	if err != nil {
		return nil, err
	}
	indexBefore, err := a.readIndexEvidence(worktree)
	if err != nil {
		return nil, err
	}
	flagsBefore, flagsTokenBefore, err := a.readIndexFlags(ctx, builder)
	if err != nil {
		return nil, err
	}
	if captureFlagsRequireNonCandidate(flagsBefore) {
		return nil, &GitError{Code: ErrorLocalCaptureIncomplete}
	}
	statusData, err := a.runBoundedRecord(ctx, builder, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		return nil, err
	}
	untrackedData, err := a.runBoundedRecord(ctx, builder, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	statusEntries, untrackedPaths, err := parseStatusRecords(statusData, untrackedData)
	if err != nil {
		return nil, err
	}
	statusToken := digestParts(statusData, untrackedData)
	if err := a.materializeUntracked(worktree.RootPath, statusEntries, untrackedPaths); err != nil {
		return nil, err
	}
	renameRecords, renameEvidence, err := a.readRenameEvidence(ctx, builder, base.ObjectID, statusEntries)
	if err != nil {
		return nil, err
	}
	conversionEvidence, err := a.readConversionEvidence(ctx, builder, statusEntries)
	if err != nil {
		return nil, err
	}
	if err := validateCaptureCounts(a.policy, len(statusEntries), len(statusData)+len(untrackedData)); err != nil {
		return nil, err
	}
	plan, err := a.capturePlan(repo, len(statusEntries), len(statusData)+len(untrackedData), indexBefore.Bytes)
	if err != nil {
		return nil, err
	}
	reservation, err := a.capacity.Reserve(ctx, plan, a.policy, a.volumeEvidence)
	if err != nil {
		return nil, err
	}
	releaseReservation := true
	defer func() {
		if releaseReservation {
			_ = a.capacity.Release(context.Background(), reservation, plan, a.policy)
		}
	}()
	patchSpool, err := a.spools.Create(ctx, app.SpoolSpec{OperationID: a.operationID, OwnerKind: app.OwnerCapture, Reservation: reservation, Limits: a.spoolLimits()})
	if err != nil {
		return nil, err
	}
	blobSpool, err := a.spools.Create(ctx, app.SpoolSpec{OperationID: a.operationID, OwnerKind: app.OwnerCapture, Reservation: reservation, Limits: a.spoolLimits()})
	if err != nil {
		_ = patchSpool.Abort(context.Background())
		return nil, err
	}
	cleanupSpools := true
	defer func() {
		if cleanupSpools {
			_ = patchSpool.Abort(context.Background())
			_ = blobSpool.Abort(context.Background())
		}
	}()
	patchFileValue, err := patchSpool.CreateFile(ctx, "patch")
	if err != nil {
		return nil, err
	}
	patchOpen := true
	defer func() {
		if patchOpen {
			_ = patchFileValue.Close()
		}
	}()
	patchWriter := &captureHashWriter{writer: patchFileValue, hash: sha256.New()}
	patchArgs, err := a.patchPolicy.DiffArgs(a.renamePolicy)
	if err != nil {
		return nil, err
	}
	patchConfig, err := a.patchPolicy.DiffConfigArgs()
	if err != nil {
		return nil, err
	}
	conversionConfig, err := a.conversionPolicy.ConfigArgs()
	if err != nil {
		return nil, err
	}
	controlledDiffConfig := append(append([]string(nil), patchConfig...), conversionConfig...)
	patchOptions := patchArgs[len(patchConfig):]
	trackedArgs := append(append(append([]string(nil), controlledDiffConfig...), "diff"), patchOptions...)
	trackedArgs = append(trackedArgs, string(base.ObjectID), "--")
	if _, streamErr := builder.RunStream(ctx, patchWriter, trackedArgs...); streamErr != nil {
		_ = patchFileValue.Close()
		patchOpen = false
		return nil, classifyCaptureError(streamErr)
	}
	for _, path := range untrackedPaths {
		untrackedArgs := append(append(append([]string(nil), controlledDiffConfig...), "diff"), patchOptions...)
		untrackedArgs = append(untrackedArgs, "--no-index", os.DevNull, "--", string(path))
		if _, streamErr := builder.RunStream(ctx, patchWriter, untrackedArgs...); streamErr != nil && !acceptNoIndexDifference(streamErr) {
			_ = patchFileValue.Close()
			patchOpen = false
			return nil, classifyCaptureError(streamErr)
		}
	}
	if err := patchFileValue.Close(); err != nil {
		patchOpen = false
		return nil, err
	}
	patchOpen = false
	patchIdentity, err := patchSpool.CloseAndVerify(ctx)
	if err != nil {
		return nil, err
	}

	blobRecords, filesystemToken, err := a.captureEntryBlobs(ctx, builder, worktree.RootPath, statusEntries, blobSpool)
	if err != nil {
		return nil, err
	}
	blobIdentity, err := blobSpool.CloseAndVerify(ctx)
	if err != nil {
		return nil, err
	}
	entries := attachBlobRefs(statusEntries, blobRecords, blobIdentity)
	if renameEvidence.AnchorMappingAllowed() {
		entries = applyRenameRecords(entries, renameRecords, a.renamePolicy.Version)
	}
	if err := a.revalidateWorkingBlobs(worktree.RootPath, blobRecords); err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool { return captureEntryPath(entries[i]) < captureEntryPath(entries[j]) })
	indexAfter, err := a.readIndexEvidence(worktree)
	if err != nil {
		return nil, err
	}
	flagsAfter, flagsTokenAfter, err := a.readIndexFlags(ctx, builder)
	if err != nil {
		return nil, err
	}
	baseAfter, headTokenAfter, err := a.captureBase(ctx, builder)
	if err != nil {
		return nil, err
	}
	statusAfter, err := a.runBoundedRecord(ctx, builder, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--no-renames", "--ignore-submodules=all")
	if err != nil {
		return nil, err
	}
	untrackedAfter, err := a.runBoundedRecord(ctx, builder, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	if headToken != headTokenAfter || base.ObjectID != baseAfter.ObjectID || indexBefore.SHA256 != indexAfter.SHA256 || indexBefore.Exists != indexAfter.Exists || flagsTokenBefore != flagsTokenAfter || statusToken != digestParts(statusAfter, untrackedAfter) {
		return nil, &GitError{Code: ErrorLocalCaptureMutation}
	}
	if !equalIndexFlags(flagsBefore, flagsAfter) {
		return nil, &GitError{Code: ErrorLocalCaptureMutation}
	}
	policyEvidence, err := newCapturePolicyEvidence(a, renameEvidence, conversionEvidence)
	if err != nil {
		return nil, err
	}
	consistency := repository.CaptureConsistencyEvidence{
		HeadToken:       headToken,
		IndexToken:      digestIndex(indexBefore),
		StatusToken:     statusToken,
		FlagsToken:      flagsTokenBefore,
		FilesystemToken: filesystemToken,
	}
	candidate := repository.LocalCaptureCandidate{
		Version:      repository.LocalCaptureCandidateVersion,
		RepositoryID: repo.ID,
		WorktreeID:   worktree.ID,
		Base:         base,
		Index:        mergeIndexEvidence(indexBefore, indexAfter, flagsBefore),
		Entries:      entries,
		Patch: repository.CaptureArtifact{
			Kind:          repository.CaptureArtifactPatch,
			SpoolID:       patchIdentity.SpoolID,
			ManifestHash:  patchIdentity.ManifestHash,
			RelativePath:  "patch",
			Bytes:         uint64(patchWriter.bytes),
			Entries:       1,
			ContentSHA256: hex.EncodeToString(patchWriter.hash.Sum(nil)),
			VerifiedAt:    patchIdentity.VerifiedAt,
		},
		BlobSpool: repository.CaptureArtifact{
			Kind:         repository.CaptureArtifactBlobs,
			SpoolID:      blobIdentity.SpoolID,
			ManifestHash: blobIdentity.ManifestHash,
			RelativePath: "payload",
			Bytes:        uint64(blobIdentity.Bytes),
			Entries:      uint64(blobIdentity.Entries),
			VerifiedAt:   blobIdentity.VerifiedAt,
		},
		Policy:      policyEvidence,
		Consistency: consistency,
		EntryCount:  uint64(len(entries)),
		TotalBytes:  uint64(patchWriter.bytes) + uint64(blobIdentity.Bytes),
		CapturedAt:  time.Now().UTC(),
	}
	fingerprint, err := candidate.FingerprintValue()
	if err != nil {
		return nil, err
	}
	candidate.Fingerprint = fingerprint
	consistency.AggregateToken = fingerprint
	candidate.Consistency = consistency
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	releaseReservation = false
	cleanupSpools = false
	return &LocalCaptureResult{Candidate: candidate, PatchSpool: patchSpool, BlobSpool: blobSpool, Reservation: reservation, Plan: plan, Policy: a.policy, capacity: a.capacity, policy: a.policy}, nil
}

type captureStatusRecord struct {
	entry      repository.LocalCaptureEntry
	baseID     *repository.ObjectID
	indexID    *repository.ObjectID
	stageIDs   [3]*repository.ObjectID
	stageModes [3]uint32
	untracked  bool
}

type captureBlobRecord struct {
	entryKey      string
	side          repository.CaptureBlobSide
	path          repository.RepoPath
	relative      string
	identity      app.StreamIdentity
	contentClass  repository.ContentClassV1
	textSemantics *repository.TextByteSemantics
}

type indexReadEvidence struct {
	Exists bool
	Bytes  uint64
	SHA256 string
}

func (a *LocalCaptureAdapter) captureBase(ctx context.Context, builder *CommandBuilder) (repository.LocalCaptureBase, string, error) {
	formatResult, err := builder.Run(ctx, "rev-parse", "--show-object-format=storage")
	if err != nil {
		return repository.LocalCaptureBase{}, "", err
	}
	objectFormat, err := parseOutputLine("object format", formatResult.Stdout, false)
	if err != nil {
		return repository.LocalCaptureBase{}, "", err
	}
	headResult, headErr := builder.Run(ctx, "rev-parse", "--verify", "HEAD")
	var head repository.ObjectID
	unborn := false
	if headErr != nil {
		var gitErr *GitError
		if !errors.As(headErr, &gitErr) || gitErr.Code != ErrorCommandFailed || !isUnbornHeadError(gitErr.Stderr) {
			if errors.As(headErr, &gitErr) && gitErr.Code == ErrorCommandFailed && gitErr.ExitCode == 128 {
				return repository.LocalCaptureBase{}, "", &GitError{Code: ErrorObjectUnavailableNoFetch, Cause: headErr, ExitCode: gitErr.ExitCode, Stderr: gitErr.Stderr}
			}
			return repository.LocalCaptureBase{}, "", headErr
		}
		unborn = true
	} else {
		value, parseErr := parseCaptureObjectLine("HEAD", headResult.Stdout)
		if parseErr != nil {
			return repository.LocalCaptureBase{}, "", parseErr
		}
		head, err = repository.NewObjectID(value)
		if err != nil || strings.Trim(value, "0") == "" {
			return repository.LocalCaptureBase{}, "", malformed("invalid HEAD object ID")
		}
	}
	if unborn {
		emptyResult, emptyErr := builder.RunInput(ctx, strings.NewReader(""), EmptyTreeArgs()...)
		if emptyErr != nil {
			return repository.LocalCaptureBase{}, "", emptyErr
		}
		value, parseErr := parseCaptureObjectLine("empty tree", emptyResult.Stdout)
		if parseErr != nil {
			return repository.LocalCaptureBase{}, "", parseErr
		}
		head, err = repository.NewObjectID(value)
		if err != nil || strings.Trim(value, "0") == "" {
			return repository.LocalCaptureBase{}, "", malformed("invalid empty tree object ID")
		}
	}
	base := repository.LocalCaptureBase{ObjectFormat: objectFormat, ObjectID: head, Unborn: unborn}
	if err := base.Validate(); err != nil {
		return repository.LocalCaptureBase{}, "", err
	}
	return base, digestParts([]byte(objectFormat), []byte(head), []byte(strconv.FormatBool(unborn))), nil
}

func (a *LocalCaptureAdapter) runBoundedRecord(ctx context.Context, builder *CommandBuilder, command ...string) ([]byte, error) {
	result, err := builder.Run(ctx, command...)
	if err != nil {
		return nil, classifyCaptureError(err)
	}
	if uint64(len(result.Stdout)) > uint64(a.policy.Input.GitRecordBytes) {
		return nil, &GitError{Code: ErrorLocalCaptureLimit}
	}
	return result.Stdout, nil
}

func parseCaptureObjectLine(name string, data []byte) (string, error) {
	value := strings.Trim(string(data), " \t\r\n")
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", malformed("invalid %s output", name)
	}
	return value, nil
}

func (a *LocalCaptureAdapter) readIndexEvidence(worktree repository.WorktreeRef) (indexReadEvidence, error) {
	path := filepath.Join(worktree.GitDir, "index")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return indexReadEvidence{}, nil
	}
	if err != nil {
		return indexReadEvidence{}, &GitError{Code: ErrorPermission, Cause: err}
	}
	if !info.Mode().IsRegular() {
		return indexReadEvidence{}, &GitError{Code: ErrorLocalCaptureIncomplete, Cause: errors.New("index is not a regular file")}
	}
	file, err := os.Open(path)
	if err != nil {
		return indexReadEvidence{}, &GitError{Code: ErrorPermission, Cause: err}
	}
	hashValue := sha256.New()
	var bytesRead uint64
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := file.Read(buffer)
		if read > 0 {
			bytesRead += uint64(read)
			if bytesRead > uint64(a.policy.Artifact.CaptureDeltaBytes) {
				_ = file.Close()
				return indexReadEvidence{}, &GitError{Code: ErrorLocalCaptureLimit}
			}
			_, _ = hashValue.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return indexReadEvidence{}, &GitError{Code: ErrorPermission, Cause: readErr}
		}
	}
	if err := file.Close(); err != nil {
		return indexReadEvidence{}, &GitError{Code: ErrorPermission, Cause: err}
	}
	after, err := os.Lstat(path)
	if err != nil || !sameFileObservation(info, after) {
		return indexReadEvidence{}, &GitError{Code: ErrorLocalCaptureMutation, Cause: err}
	}
	return indexReadEvidence{Exists: true, Bytes: bytesRead, SHA256: hex.EncodeToString(hashValue.Sum(nil))}, nil
}

func sameFileObservation(before, after os.FileInfo) bool {
	if before == nil || after == nil || !os.SameFile(before, after) {
		return false
	}
	return before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime() == after.ModTime()
}

func (a *LocalCaptureAdapter) readIndexFlags(ctx context.Context, builder *CommandBuilder) ([]repository.CaptureIndexEntry, string, error) {
	stageData, err := a.runBoundedRecord(ctx, builder, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, "", err
	}
	verboseData, err := a.runBoundedRecord(ctx, builder, "ls-files", "-v", "-z")
	if err != nil {
		return nil, "", err
	}
	fsmonitorData, err := a.runBoundedRecord(ctx, builder, "ls-files", "-f", "-z")
	if err != nil {
		return nil, "", err
	}
	tagData, err := a.runBoundedRecord(ctx, builder, "ls-files", "-t", "-z")
	if err != nil {
		return nil, "", err
	}
	entries, err := parseIndexEntries(stageData)
	if err != nil {
		return nil, "", err
	}
	verbose, err := parseIndexTags(verboseData)
	if err != nil {
		return nil, "", err
	}
	fsmonitor, err := parseIndexTags(fsmonitorData)
	if err != nil {
		return nil, "", err
	}
	tags, err := parseIndexTags(tagData)
	if err != nil {
		return nil, "", err
	}
	for index := range entries {
		pathKey := string(entries[index].Path)
		flags := make([]repository.CaptureIndexFlag, 0, 3)
		if strings.ToLower(verbose[pathKey]) == verbose[pathKey] && verbose[pathKey] != "" {
			flags = append(flags, repository.CaptureIndexAssumeUnchanged)
		}
		if strings.ToLower(fsmonitor[pathKey]) == fsmonitor[pathKey] && fsmonitor[pathKey] != "" {
			flags = append(flags, repository.CaptureIndexFSMonitorValid)
		}
		if tags[pathKey] == "S" {
			flags = append(flags, repository.CaptureIndexSkipWorktree, repository.CaptureIndexSparse)
		}
		if allZeroObjectID(entries[index].ObjectID) {
			flags = append(flags, repository.CaptureIndexIntentToAdd)
		}
		sort.Slice(flags, func(i, j int) bool { return flags[i] < flags[j] })
		entries[index].Flags = uniqueCaptureFlags(flags)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if string(entries[i].Path) != string(entries[j].Path) {
			return string(entries[i].Path) < string(entries[j].Path)
		}
		return entries[i].Stage < entries[j].Stage
	})
	return entries, digestIndexEntries(entries), nil
}

func parseIndexEntries(data []byte) ([]repository.CaptureIndexEntry, error) {
	var entries []repository.CaptureIndexEntry
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		separator := bytes.IndexByte(record, '\t')
		if separator <= 0 {
			return nil, malformed("index stage record")
		}
		fields := strings.Fields(string(record[:separator]))
		if len(fields) != 3 {
			return nil, malformed("index stage fields")
		}
		mode, err := parseGitMode(fields[0])
		if err != nil || mode == 0 {
			return nil, malformed("index stage mode")
		}
		objectID, err := repository.NewObjectID(fields[1])
		if err != nil {
			return nil, malformed("index stage object")
		}
		stage, err := strconv.ParseUint(fields[2], 10, 8)
		if err != nil || stage > 3 {
			return nil, malformed("index stage number")
		}
		path, err := repository.NewRepoPath(record[separator+1:])
		if err != nil {
			return nil, malformed("index stage path")
		}
		entry := repository.CaptureIndexEntry{Path: path, Stage: uint8(stage), Mode: mode, ObjectID: objectID}
		if err := entry.Validate(); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseIndexTags(data []byte) (map[string]string, error) {
	tags := make(map[string]string)
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) < 3 || record[1] != ' ' {
			if len(record) == 0 {
				continue
			}
			return nil, malformed("index flag record")
		}
		path, err := repository.NewRepoPath(record[2:])
		if err != nil {
			return nil, malformed("index flag path")
		}
		tags[string(path)] = string(record[:1])
	}
	return tags, nil
}

func parseGitMode(value string) (uint32, error) {
	if value == "." || value == "000000" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	return uint32(parsed), err
}

func allZeroObjectID(value repository.ObjectID) bool {
	return value != "" && strings.Trim(string(value), "0") == ""
}

func uniqueCaptureFlags(flags []repository.CaptureIndexFlag) []repository.CaptureIndexFlag {
	result := flags[:0]
	for _, flag := range flags {
		if len(result) == 0 || result[len(result)-1] != flag {
			result = append(result, flag)
		}
	}
	return result
}

func digestIndexEntries(entries []repository.CaptureIndexEntry) string {
	hashValue := sha256.New()
	for _, entry := range entries {
		_, _ = hashValue.Write([]byte(string(entry.Path)))
		_, _ = hashValue.Write([]byte{0, entry.Stage})
		_, _ = hashValue.Write([]byte(strconv.FormatUint(uint64(entry.Mode), 10)))
		_, _ = hashValue.Write([]byte{0})
		_, _ = hashValue.Write([]byte(entry.ObjectID))
		for _, flag := range entry.Flags {
			_, _ = hashValue.Write([]byte{0})
			_, _ = hashValue.Write([]byte(flag))
		}
		_, _ = hashValue.Write([]byte{0})
	}
	return hex.EncodeToString(hashValue.Sum(nil))
}

func digestIndex(value indexReadEvidence) string {
	return digestParts([]byte(strconv.FormatBool(value.Exists)), []byte(strconv.FormatUint(value.Bytes, 10)), []byte(value.SHA256))
}

func mergeIndexEvidence(before, after indexReadEvidence, entries []repository.CaptureIndexEntry) repository.LocalCaptureIndexEvidence {
	sparse := false
	for _, entry := range entries {
		for _, flag := range entry.Flags {
			if flag == repository.CaptureIndexSparse {
				sparse = true
			}
		}
	}
	return repository.LocalCaptureIndexEvidence{
		Exists:       before.Exists,
		Bytes:        before.Bytes,
		SHA256:       before.SHA256,
		Entries:      append([]repository.CaptureIndexEntry(nil), entries...),
		Sparse:       sparse,
		BeforeSHA256: before.SHA256,
		AfterSHA256:  after.SHA256,
	}
}

func equalIndexFlags(left, right []repository.CaptureIndexEntry) bool {
	return digestIndexEntries(left) == digestIndexEntries(right)
}

func captureFlagsRequireNonCandidate(entries []repository.CaptureIndexEntry) bool {
	for _, entry := range entries {
		for _, flag := range entry.Flags {
			switch flag {
			case repository.CaptureIndexAssumeUnchanged, repository.CaptureIndexSkipWorktree, repository.CaptureIndexFSMonitorValid, repository.CaptureIndexSparse:
				return true
			}
		}
	}
	return false
}

func parseStatusRecords(statusData, untrackedData []byte) ([]captureStatusRecord, []repository.RepoPath, error) {
	var records []captureStatusRecord
	statusUntracked := make(map[string]struct{})
	parts := bytes.Split(statusData, []byte{0})
	for index := 0; index < len(parts); index++ {
		record := parts[index]
		if len(record) == 0 {
			continue
		}
		if record[0] == '?' {
			path, err := statusPath(record[2:])
			if err != nil {
				return nil, nil, err
			}
			statusUntracked[string(path)] = struct{}{}
			entry := repository.LocalCaptureEntry{Change: repository.ChangedFile{NewPath: &path, Kind: repository.ChangeUntracked}}
			records = append(records, captureStatusRecord{entry: entry, untracked: true})
			continue
		}
		if record[0] == '2' {
			fields, err := splitStatusFields(record, 10)
			if err != nil || index+1 >= len(parts) {
				return nil, nil, malformed("rename status record")
			}
			oldPath, err := repository.NewRepoPath(parts[index+1])
			if err != nil {
				return nil, nil, malformed("rename old path")
			}
			index++
			entry, baseID, indexID, err := makeOrdinaryStatusEntry(fields, oldPath)
			if err != nil {
				return nil, nil, err
			}
			entry.Change.Kind = repository.ChangeRenamed
			if len(fields[8]) > 0 && fields[8][0] == 'C' {
				entry.Change.Kind = repository.ChangeCopied
			}
			records = append(records, captureStatusRecord{entry: entry, baseID: baseID, indexID: indexID})
			continue
		}
		if record[0] == '1' {
			fields, err := splitStatusFields(record, 9)
			if err != nil {
				return nil, nil, malformed("ordinary status record")
			}
			entry, baseID, indexID, err := makeOrdinaryStatusEntry(fields, nil)
			if err != nil {
				return nil, nil, err
			}
			if entry.Change.OldPath == nil && entry.Change.NewPath == nil {
				continue
			}
			records = append(records, captureStatusRecord{entry: entry, baseID: baseID, indexID: indexID})
			continue
		}
		if record[0] == 'u' {
			fields, err := splitStatusFields(record, 11)
			if err != nil {
				return nil, nil, malformed("unmerged status record")
			}
			record, err := makeUnmergedStatusEntry(fields)
			if err != nil {
				return nil, nil, err
			}
			records = append(records, record)
			continue
		}
		return nil, nil, malformed("unknown status record")
	}

	var untracked []repository.RepoPath
	seenUntracked := make(map[string]struct{})
	for _, record := range bytes.Split(untrackedData, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		path, err := repository.NewRepoPath(record)
		if err != nil {
			return nil, nil, malformed("untracked path")
		}
		if _, ok := seenUntracked[string(path)]; ok {
			return nil, nil, malformed("duplicate untracked path")
		}
		seenUntracked[string(path)] = struct{}{}
		untracked = append(untracked, path)
		if _, ok := statusUntracked[string(path)]; !ok {
			return nil, nil, &GitError{Code: ErrorLocalCaptureMutation}
		}
	}
	if len(untracked) != len(statusUntracked) {
		return nil, nil, &GitError{Code: ErrorLocalCaptureMutation}
	}
	sort.Slice(untracked, func(i, j int) bool { return string(untracked[i]) < string(untracked[j]) })
	return records, untracked, nil
}

func splitStatusFields(record []byte, count int) ([][]byte, error) {
	fields := make([][]byte, 0, count)
	remaining := record
	for len(fields) < count-1 {
		separator := bytes.IndexByte(remaining, ' ')
		if separator < 0 {
			return nil, ErrInvalidGitPolicy
		}
		fields = append(fields, remaining[:separator])
		remaining = remaining[separator+1:]
	}
	fields = append(fields, remaining)
	return fields, nil
}

func statusPath(raw []byte) (repository.RepoPath, error) {
	if len(raw) == 0 {
		return nil, malformed("empty status path")
	}
	return repository.NewRepoPath(raw)
}

func makeOrdinaryStatusEntry(fields [][]byte, renameOld repository.RepoPath) (repository.LocalCaptureEntry, *repository.ObjectID, *repository.ObjectID, error) {
	if (len(fields) != 9 && len(fields) != 10) || len(fields[1]) != 2 {
		return repository.LocalCaptureEntry{}, nil, nil, malformed("ordinary status fields")
	}
	xy := string(fields[1])
	modes := make([]uint32, 3)
	for index := range modes {
		mode, err := parseGitMode(string(fields[index+3]))
		if err != nil {
			return repository.LocalCaptureEntry{}, nil, nil, malformed("status mode")
		}
		modes[index] = mode
	}
	baseID, err := parseStatusObject(fields[6])
	if err != nil {
		return repository.LocalCaptureEntry{}, nil, nil, err
	}
	indexID, err := parseStatusObject(fields[7])
	if err != nil {
		return repository.LocalCaptureEntry{}, nil, nil, err
	}
	newPath, err := repository.NewRepoPath(fields[len(fields)-1])
	if err != nil {
		return repository.LocalCaptureEntry{}, nil, nil, malformed("status path")
	}
	oldPath := newPath
	if renameOld != nil {
		oldPath = renameOld
	}
	oldPresent := modes[0] != 0 || baseID != nil
	newPresent := modes[2] != 0
	change := repository.ChangedFile{Staged: statusChanged(xy[0]), Unstaged: statusChanged(xy[1])}
	if oldPresent {
		change.OldPath = &oldPath
		change.OldFileKind = fileKindFromGitMode(modes[0])
		change.OldMode = modes[0]
		change.OldObjectID = baseID
	}
	if newPresent {
		change.NewPath = &newPath
		change.NewFileKind = fileKindFromGitMode(modes[2])
		change.NewMode = modes[2]
	}
	switch {
	case !oldPresent && newPresent:
		change.Kind = repository.ChangeAdded
	case oldPresent && !newPresent:
		change.Kind = repository.ChangeDeleted
	case oldPresent && newPresent && change.OldFileKind != change.NewFileKind:
		change.Kind = repository.ChangeTypeChanged
	case oldPresent && newPresent:
		change.Kind = repository.ChangeModified
	default:
		return repository.LocalCaptureEntry{Change: change}, baseID, indexID, nil
	}
	return repository.LocalCaptureEntry{Change: change}, baseID, indexID, nil
}

func makeUnmergedStatusEntry(fields [][]byte) (captureStatusRecord, error) {
	if len(fields) != 11 || len(fields[1]) != 2 {
		return captureStatusRecord{}, malformed("unmerged status fields")
	}
	modes := [4]uint32{}
	for index := range modes {
		mode, err := parseGitMode(string(fields[index+3]))
		if err != nil {
			return captureStatusRecord{}, malformed("unmerged mode")
		}
		modes[index] = mode
	}
	ids := [3]*repository.ObjectID{}
	for index := range ids {
		id, err := parseStatusObject(fields[index+7])
		if err != nil {
			return captureStatusRecord{}, err
		}
		ids[index] = id
	}
	path, err := repository.NewRepoPath(fields[10])
	if err != nil {
		return captureStatusRecord{}, malformed("unmerged path")
	}
	if modes[0] == 0 || ids[0] == nil || (modes[1] == 0 || ids[1] == nil) && (modes[2] == 0 || ids[2] == nil) {
		return captureStatusRecord{}, &GitError{Code: ErrorLocalCaptureIncomplete, Cause: repository.ErrInvalidConflictEvidence}
	}
	stage := func(index int) *repository.IndexStage {
		if modes[index] == 0 || ids[index] == nil {
			return nil
		}
		return &repository.IndexStage{Mode: modes[index], ObjectID: *ids[index]}
	}
	conflict := &repository.IndexConflictEvidence{Code: "u", Stage1: stage(0), Stage2: stage(1), Stage3: stage(2)}
	if err := conflict.Validate(); err != nil {
		return captureStatusRecord{}, &GitError{Code: ErrorLocalCaptureIncomplete, Cause: err}
	}
	change := repository.ChangedFile{
		Kind:        repository.ChangeModified,
		Staged:      statusChanged(fields[1][0]),
		Unstaged:    statusChanged(fields[1][1]),
		Conflict:    conflict,
		OldPath:     &path,
		NewPath:     &path,
		OldMode:     modes[0],
		OldFileKind: fileKindFromGitMode(modes[0]),
		NewMode:     modes[3],
		NewFileKind: fileKindFromGitMode(modes[3]),
	}
	if modes[3] == 0 {
		change.Kind = repository.ChangeDeleted
		change.NewPath = nil
		change.NewFileKind = ""
	}
	return captureStatusRecord{entry: repository.LocalCaptureEntry{Change: change}, stageIDs: ids, stageModes: [3]uint32{modes[0], modes[1], modes[2]}}, nil
}

func parseStatusObject(raw []byte) (*repository.ObjectID, error) {
	value := string(raw)
	if value == "" || value == "." || allZeroText(value) {
		return nil, nil
	}
	id, err := repository.NewObjectID(value)
	if err != nil {
		return nil, malformed("status object ID")
	}
	return &id, nil
}

func allZeroText(value string) bool {
	return value != "" && strings.Trim(value, "0") == ""
}

func statusChanged(value byte) bool {
	return value != '.' && value != ' '
}

func fileKindFromGitMode(mode uint32) repository.FileKind {
	switch mode & 0170000 {
	case 0100000:
		return repository.FileKindRegular
	case 0120000:
		return repository.FileKindSymlink
	case 0160000:
		return repository.FileKindGitlink
	case 0040000:
		return repository.FileKindDirectory
	case 0:
		return repository.FileKindUnknown
	default:
		return repository.FileKindUnknown
	}
}

func (a *LocalCaptureAdapter) materializeUntracked(root string, records []captureStatusRecord, untracked []repository.RepoPath) error {
	allowed := make(map[string]struct{}, len(untracked))
	for _, path := range untracked {
		allowed[string(path)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(records))
	for index := range records {
		if !records[index].untracked {
			continue
		}
		path := *records[index].entry.Change.NewPath
		if _, ok := allowed[string(path)]; !ok {
			return &GitError{Code: ErrorLocalCaptureMutation}
		}
		kind, mode, err := observeWorkingPath(root, path)
		if err != nil {
			return err
		}
		records[index].entry.Change.NewFileKind = kind
		records[index].entry.Change.NewMode = mode
		seen[string(path)] = struct{}{}
	}
	if len(seen) != len(allowed) {
		return &GitError{Code: ErrorLocalCaptureMutation}
	}
	return nil
}

func (a *LocalCaptureAdapter) readConversionEvidence(ctx context.Context, builder *CommandBuilder, entries []captureStatusRecord) (ContentConversionEvidenceV1, error) {
	paths := make(map[string]repository.RepoPath)
	attributesChanged := false
	for _, entry := range entries {
		for _, path := range []*repository.RepoPath{entry.entry.Change.OldPath, entry.entry.Change.NewPath} {
			if path == nil {
				continue
			}
			paths[string(*path)] = *path
			components := strings.Split(string(*path), "/")
			if len(components) > 0 && components[len(components)-1] == ".gitattributes" {
				attributesChanged = true
			}
		}
	}
	if uint64(len(paths)) > uint64(a.policy.Artifact.CaptureEntries) {
		return ContentConversionEvidenceV1{}, &GitError{Code: ErrorLocalCaptureLimit}
	}
	observations := make([]AttributeObservation, 0)
	for _, path := range paths {
		result, err := a.runBoundedRecord(ctx, builder, "check-attr", "--all", "-z", "--", string(path))
		if err != nil {
			return ContentConversionEvidenceV1{}, err
		}
		parsed, err := parseAttributeRecords(result, path)
		if err != nil {
			return ContentConversionEvidenceV1{}, err
		}
		observations = append(observations, parsed...)
	}
	return a.conversionPolicy.Evaluate(observations, attributesChanged)
}

func parseAttributeRecords(data []byte, expectedPath repository.RepoPath) ([]AttributeObservation, error) {
	parts := bytes.Split(data, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts)%3 != 0 {
		return nil, malformed("attribute output")
	}
	observations := make([]AttributeObservation, 0, len(parts)/3)
	for index := 0; index < len(parts); index += 3 {
		if !bytes.Equal(parts[index], expectedPath) || len(parts[index+1]) == 0 {
			return nil, malformed("attribute path")
		}
		name := AttributeName(string(parts[index+1]))
		value := string(parts[index+2])
		state := AttributeValue
		switch value {
		case "unspecified":
			state = AttributeUnspecified
			value = ""
		case "unset":
			state = AttributeUnset
			value = ""
		case "set":
			state = AttributeSet
			value = ""
		}
		observation := AttributeObservation{Path: expectedPath, Name: name, State: state, Value: value, Source: AttributeSourceRepository}
		if err := observation.Validate(); err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func observeWorkingPath(root string, path repository.RepoPath) (repository.FileKind, uint32, error) {
	native, err := captureNativePath(root, path)
	if err != nil {
		return repository.FileKindUnknown, 0, err
	}
	info, err := os.Lstat(native)
	if err != nil {
		return repository.FileKindUnknown, 0, &GitError{Code: ErrorLocalCaptureIncomplete, Cause: err}
	}
	kind, mode := fileKindFromNativeInfo(info)
	if kind != repository.FileKindRegular && kind != repository.FileKindSymlink {
		return repository.FileKindUnknown, 0, &GitError{Code: ErrorLocalCaptureIncomplete, Cause: errors.New("unsupported untracked filesystem kind")}
	}
	return kind, mode, nil
}

type renameCaptureRecord struct {
	Kind              byte
	SimilarityPercent uint8
	OldPath           repository.RepoPath
	NewPath           repository.RepoPath
}

func (a *LocalCaptureAdapter) readRenameEvidence(ctx context.Context, builder *CommandBuilder, base repository.ObjectID, entries []captureStatusRecord) ([]renameCaptureRecord, RenamePolicyEvidenceV1, error) {
	config, err := a.patchPolicy.DiffConfigArgs()
	if err != nil {
		return nil, RenamePolicyEvidenceV1{}, err
	}
	conversionConfig, err := a.conversionPolicy.ConfigArgs()
	if err != nil {
		return nil, RenamePolicyEvidenceV1{}, err
	}
	config = append(config, conversionConfig...)
	renameArgs, err := a.renamePolicy.DiffArgs()
	if err != nil {
		return nil, RenamePolicyEvidenceV1{}, err
	}
	args := append(append([]string(nil), config...), "diff", "--name-status", "-z")
	args = append(args, renameArgs...)
	args = append(args, string(base), "--")
	data, err := a.runBoundedRecord(ctx, builder, args...)
	if err != nil {
		return nil, RenamePolicyEvidenceV1{}, err
	}
	records, _, _, err := parseRenameRecords(data)
	if err != nil {
		return nil, RenamePolicyEvidenceV1{}, err
	}
	deleteCandidates, addCandidates := 0, 0
	for _, entry := range entries {
		switch entry.entry.Change.Kind {
		case repository.ChangeDeleted:
			deleteCandidates++
		case repository.ChangeAdded, repository.ChangeUntracked:
			addCandidates++
		}
	}
	if deleteCandidates > a.renamePolicy.MaxDeleteSources {
		deleteCandidates = a.renamePolicy.MaxDeleteSources
	}
	if addCandidates > a.renamePolicy.MaxAddTargets {
		addCandidates = a.renamePolicy.MaxAddTargets
	}
	outcome := RenameDetectionComplete
	if deleteCandidates >= a.renamePolicy.MaxDeleteSources || addCandidates >= a.renamePolicy.MaxAddTargets {
		outcome = RenameDetectionLimited
	}
	evidence, err := NewRenamePolicyEvidence(a.renamePolicy, outcome, deleteCandidates, addCandidates)
	if err != nil {
		return nil, RenamePolicyEvidenceV1{}, err
	}
	return records, evidence, nil
}

func parseRenameRecords(data []byte) ([]renameCaptureRecord, int, int, error) {
	parts := bytes.Split(data, []byte{0})
	var records []renameCaptureRecord
	deleteCandidates, addCandidates := 0, 0
	for index := 0; index < len(parts); index++ {
		value := parts[index]
		if len(value) == 0 {
			continue
		}
		status := value
		var path repository.RepoPath
		if tab := bytes.IndexByte(value, '\t'); tab >= 0 {
			status = value[:tab]
			parsed, err := repository.NewRepoPath(value[tab+1:])
			if err != nil {
				return nil, 0, 0, malformed("name-status path")
			}
			path = parsed
		} else if value[0] != 'R' && value[0] != 'C' {
			pathValue := value[1:]
			if len(pathValue) == 0 {
				if index+1 >= len(parts) {
					return nil, 0, 0, malformed("name-status path")
				}
				pathValue = parts[index+1]
				index++
			}
			parsed, err := repository.NewRepoPath(pathValue)
			if err != nil {
				return nil, 0, 0, malformed("name-status path")
			}
			status = value[:1]
			path = parsed
		}
		if len(status) == 0 {
			return nil, 0, 0, malformed("empty name-status")
		}
		switch status[0] {
		case 'D':
			deleteCandidates++
		case 'A':
			addCandidates++
		case 'R', 'C':
			if index+2 >= len(parts) {
				return nil, 0, 0, malformed("rename name-status paths")
			}
			similarity, scoreErr := parseRenameScore(status[1:])
			if scoreErr != nil {
				return nil, 0, 0, scoreErr
			}
			oldPath, oldErr := repository.NewRepoPath(parts[index+1])
			newPath, newErr := repository.NewRepoPath(parts[index+2])
			if oldErr != nil || newErr != nil {
				return nil, 0, 0, malformed("rename name-status paths")
			}
			records = append(records, renameCaptureRecord{Kind: status[0], SimilarityPercent: similarity, OldPath: oldPath, NewPath: newPath})
			index += 2
			deleteCandidates++
			addCandidates++
		default:
			if path == nil {
				return nil, 0, 0, malformed("name-status path")
			}
		}
	}
	return records, deleteCandidates, addCandidates, nil
}

func parseRenameScore(value []byte) (uint8, error) {
	if len(value) == 0 {
		return 100, nil
	}
	parsed := 0
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, malformed("rename similarity score")
		}
		parsed = parsed*10 + int(digit-'0')
		if parsed > 100 {
			return 0, malformed("rename similarity score")
		}
	}
	if parsed < 60 {
		return 0, malformed("rename similarity below policy threshold")
	}
	return uint8(parsed), nil
}

func applyRenameRecords(entries []repository.LocalCaptureEntry, renames []renameCaptureRecord, policyVersion uint32) []repository.LocalCaptureEntry {
	for _, rename := range renames {
		oldIndex, newIndex := -1, -1
		for index := range entries {
			if entries[index].Change.NewPath != nil && string(*entries[index].Change.NewPath) == string(rename.NewPath) {
				newIndex = index
			}
			if entries[index].Change.NewPath != nil && string(*entries[index].Change.NewPath) == string(rename.OldPath) || entries[index].Change.OldPath != nil && string(*entries[index].Change.OldPath) == string(rename.OldPath) {
				oldIndex = index
			}
		}
		if newIndex < 0 || oldIndex < 0 || oldIndex == newIndex {
			continue
		}
		old := entries[oldIndex]
		current := entries[newIndex]
		oldPath := rename.OldPath
		current.Change.OldPath = &oldPath
		current.Change.OldFileKind = old.Change.OldFileKind
		current.Change.OldMode = old.Change.OldMode
		current.Change.OldObjectID = old.Change.OldObjectID
		if rename.Kind == 'C' {
			current.Change.Kind = repository.ChangeCopied
		} else {
			current.Change.Kind = repository.ChangeRenamed
		}
		kind := current.Change.Kind
		evidence, err := repository.NewRenameEvidence(policyVersion, rename.SimilarityPercent, kind, rename.OldPath, rename.NewPath)
		if err != nil {
			continue
		}
		current.Change.Rename = &evidence
		for _, blob := range old.Blobs {
			if blob.Side == repository.CaptureBlobBase {
				current.Blobs = append(current.Blobs, blob)
			}
		}
		entries[newIndex] = current
		if rename.Kind == 'R' {
			entries = append(entries[:oldIndex], entries[oldIndex+1:]...)
			if oldIndex < newIndex {
				newIndex--
			}
		}
	}
	return entries
}

func newCapturePolicyEvidence(a *LocalCaptureAdapter, rename RenamePolicyEvidenceV1, conversion ContentConversionEvidenceV1) (repository.CapturePolicyEvidence, error) {
	if err := rename.Validate(); err != nil {
		return repository.CapturePolicyEvidence{}, err
	}
	if err := conversion.Validate(); err != nil {
		return repository.CapturePolicyEvidence{}, err
	}
	return repository.CapturePolicyEvidence{
		MachineGitVersion:               MachineGitReadPolicyVersion,
		RenameVersion:                   rename.Policy.Version,
		RenameOutcome:                   string(rename.Outcome),
		RenameDeleteCandidates:          uint64(rename.DeleteCandidates),
		RenameAddCandidates:             uint64(rename.AddCandidates),
		RenameSimilarityPercent:         rename.Policy.SimilarityPercent,
		RenameMaxDeleteSources:          uint64(rename.Policy.MaxDeleteSources),
		RenameMaxAddTargets:             uint64(rename.Policy.MaxAddTargets),
		RenameDetectChangedSourceCopies: rename.Policy.DetectChangedSourceCopies,
		RenameFindCopiesHarder:          rename.Policy.FindCopiesHarder,
		RenameFlags:                     append([]string(nil), rename.Flags...),
		RenameEvidenceHash:              rename.EvidenceHash,
		PatchFormatVersion:              a.patchPolicy.Version,
		ConversionPolicyVersion:         a.conversionPolicy.Version,
		ConversionDecision:              string(conversion.Decision),
		ConversionReason:                string(conversion.Reason),
		ConversionFingerprint:           conversion.Fingerprint,
		AttributesChanged:               conversion.AttributesChanged,
		ResourcePolicyVersion:           uint32(a.policy.Version),
	}, nil
}

func (a *LocalCaptureAdapter) capturePlan(repo repository.Repository, entries, recordBytes int, indexBytes uint64) (app.CapacityPlan, error) {
	if entries > int(a.policy.Artifact.CaptureEntries) || uint64(recordBytes) > uint64(a.policy.Input.GitRecordBytes) {
		return app.CapacityPlan{}, &GitError{Code: ErrorLocalCaptureLimit}
	}
	repositoryID := repo.ID
	plan := app.CapacityPlan{
		OperationID:  a.operationID,
		RepositoryID: &repositoryID,
		Artifacts:    []app.ArtifactEstimate{{Class: app.ArtifactCapture, Entries: maxCaptureCount(uint64(entries)), Bytes: a.policy.Artifact.CaptureDeltaBytes}},
		VolumePeaks: []app.VolumePeak{{
			ID:           a.volumeID,
			Inputs:       app.ByteSize(recordBytes) + app.ByteSize(indexBytes),
			Temporaries:  a.policy.Artifact.CaptureDeltaBytes,
			Finals:       a.policy.Artifact.CaptureDeltaBytes,
			AtomicOutput: a.policy.Artifact.CaptureDeltaBytes,
			Reserve:      a.policy.Storage.MinimumFreeBytes,
		}},
		Budget:        a.budget,
		PolicyVersion: a.policy.Version,
	}
	return plan, nil
}

func maxCaptureCount(value uint64) app.Count {
	if value == 0 {
		return 1
	}
	return app.Count(value)
}

func validateCaptureCounts(policy app.ResourcePolicy, entries, recordBytes int) error {
	if entries < 0 || uint64(entries) > uint64(policy.Artifact.CaptureEntries) || recordBytes < 0 || uint64(recordBytes) > uint64(policy.Input.GitRecordBytes) {
		return &GitError{Code: ErrorLocalCaptureLimit}
	}
	return nil
}

func (a *LocalCaptureAdapter) spoolLimits() app.SpoolLimits {
	limits, err := app.DefaultSpoolLimits(a.policy)
	if err != nil {
		return app.SpoolLimits{}
	}
	limits.MaxBytes = a.policy.Artifact.CaptureDeltaBytes
	limits.MaxEntries = a.policy.Artifact.CaptureEntries
	return limits
}

func digestParts(parts ...[]byte) string {
	hashValue := sha256.New()
	for _, part := range parts {
		var length [8]byte
		value := uint64(len(part))
		for index := range length {
			length[len(length)-index-1] = byte(value >> (index * 8))
		}
		_, _ = hashValue.Write(length[:])
		_, _ = hashValue.Write(part)
	}
	return hex.EncodeToString(hashValue.Sum(nil))
}

func (a *LocalCaptureAdapter) captureEntryBlobs(ctx context.Context, builder *CommandBuilder, root string, entries []captureStatusRecord, spool app.ArtifactSpoolHandle) ([]captureBlobRecord, string, error) {
	var records []captureBlobRecord
	filesystemHash := sha256.New()
	counter := 0
	for _, entry := range entries {
		entryKey := captureEntryPath(entry.entry)
		if entry.untracked {
			identity, contentClass, textSemantics, token, relative, err := a.writeWorkingBlob(ctx, root, *entry.entry.Change.NewPath, spool, counter)
			if err != nil {
				return nil, "", err
			}
			counter++
			records = append(records, captureBlobRecord{entryKey: entryKey, side: repository.CaptureBlobWorkingTree, path: *entry.entry.Change.NewPath, relative: relative, identity: identity, contentClass: contentClass, textSemantics: textSemantics})
			_, _ = filesystemHash.Write([]byte(token))
			continue
		}
		if entry.baseID != nil && entry.entry.Change.OldPath != nil && capturableFileKind(entry.entry.Change.OldFileKind) {
			identity, contentClass, textSemantics, relative, err := a.writeGitBlob(ctx, builder, *entry.baseID, spool, counter)
			if err != nil {
				return nil, "", err
			}
			counter++
			records = append(records, captureBlobRecord{entryKey: entryKey, side: repository.CaptureBlobBase, path: *entry.entry.Change.OldPath, relative: relative, identity: identity, contentClass: contentClass, textSemantics: textSemantics})
		}
		if entry.indexID != nil && entry.entry.Change.NewPath != nil && capturableFileKind(entry.entry.Change.NewFileKind) {
			identity, contentClass, textSemantics, relative, err := a.writeGitBlob(ctx, builder, *entry.indexID, spool, counter)
			if err != nil {
				return nil, "", err
			}
			counter++
			records = append(records, captureBlobRecord{entryKey: entryKey, side: repository.CaptureBlobIndex, path: *entry.entry.Change.NewPath, relative: relative, identity: identity, contentClass: contentClass, textSemantics: textSemantics})
		}
		for stageIndex, stageID := range entry.stageIDs {
			stagePath := entry.entry.Change.NewPath
			if stagePath == nil {
				stagePath = entry.entry.Change.OldPath
			}
			if stageID == nil || stagePath == nil || !capturableFileKind(fileKindFromGitMode(entry.stageModes[stageIndex])) {
				continue
			}
			identity, contentClass, textSemantics, relative, err := a.writeGitBlob(ctx, builder, *stageID, spool, counter)
			if err != nil {
				return nil, "", err
			}
			counter++
			side := repository.CaptureBlobStage1
			if stageIndex == 1 {
				side = repository.CaptureBlobStage2
			} else if stageIndex == 2 {
				side = repository.CaptureBlobStage3
			}
			records = append(records, captureBlobRecord{entryKey: entryKey, side: side, path: *stagePath, relative: relative, identity: identity, contentClass: contentClass, textSemantics: textSemantics})
		}
		if entry.entry.Change.NewPath != nil && capturableFileKind(entry.entry.Change.NewFileKind) {
			identity, contentClass, textSemantics, token, relative, err := a.writeWorkingBlob(ctx, root, *entry.entry.Change.NewPath, spool, counter)
			if err != nil {
				return nil, "", err
			}
			counter++
			records = append(records, captureBlobRecord{entryKey: entryKey, side: repository.CaptureBlobWorkingTree, path: *entry.entry.Change.NewPath, relative: relative, identity: identity, contentClass: contentClass, textSemantics: textSemantics})
			_, _ = filesystemHash.Write([]byte(token))
		}
	}
	return records, hex.EncodeToString(filesystemHash.Sum(nil)), nil
}

func capturableFileKind(kind repository.FileKind) bool {
	return kind == repository.FileKindRegular || kind == repository.FileKindSymlink
}

func (a *LocalCaptureAdapter) writeGitBlob(ctx context.Context, builder *CommandBuilder, objectID repository.ObjectID, spool app.ArtifactSpoolHandle, counter int) (app.StreamIdentity, repository.ContentClassV1, *repository.TextByteSemantics, string, error) {
	relative := fmt.Sprintf("blob-%08d", counter)
	file, err := spool.CreateFile(ctx, relative)
	if err != nil {
		return app.StreamIdentity{}, "", nil, "", err
	}
	writer := &captureHashWriter{writer: file, hash: sha256.New(), classifier: repository.NewContentClassifierV1(false), text: repository.NewTextByteSemanticsWriter()}
	_, runErr := builder.RunStream(ctx, writer, "cat-file", "blob", string(objectID))
	closeErr := file.Close()
	if runErr != nil {
		return app.StreamIdentity{}, "", nil, "", classifyCaptureError(runErr)
	}
	if closeErr != nil {
		return app.StreamIdentity{}, "", nil, "", closeErr
	}
	identity := app.StreamIdentity{Bytes: app.ByteSize(writer.bytes), SHA256: hex.EncodeToString(writer.hash.Sum(nil))}
	contentClass := writer.classifier.Classify()
	var semantics *repository.TextByteSemantics
	if contentClass == repository.ContentClassRegularTextUTF8 {
		value, semanticsErr := writer.text.Semantics(uint64(writer.bytes))
		if semanticsErr != nil {
			return app.StreamIdentity{}, "", nil, "", semanticsErr
		}
		semantics = &value
	}
	return identity, contentClass, semantics, relative, nil
}

func (a *LocalCaptureAdapter) writeWorkingBlob(ctx context.Context, root string, path repository.RepoPath, spool app.ArtifactSpoolHandle, counter int) (app.StreamIdentity, repository.ContentClassV1, *repository.TextByteSemantics, string, string, error) {
	native, err := captureNativePath(root, path)
	if err != nil {
		return app.StreamIdentity{}, "", nil, "", "", err
	}
	before, err := os.Lstat(native)
	if err != nil {
		return app.StreamIdentity{}, "", nil, "", "", &GitError{Code: ErrorLocalCaptureMutation, Cause: err}
	}
	kind, mode := fileKindFromNativeInfo(before)
	if !capturableFileKind(kind) {
		return app.StreamIdentity{}, "", nil, "", "", &GitError{Code: ErrorLocalCaptureIncomplete, Cause: errors.New("unsupported working-tree kind")}
	}
	relative := fmt.Sprintf("blob-%08d", counter)
	var identity app.StreamIdentity
	contentClass := repository.ContentClassV1("")
	var textSemantics *repository.TextByteSemantics
	var beforeTarget string
	if kind == repository.FileKindSymlink {
		target, readErr := os.Readlink(native)
		if readErr != nil {
			return app.StreamIdentity{}, "", nil, "", "", &GitError{Code: ErrorPermission, Cause: readErr}
		}
		if uint64(len(target)) > uint64(a.policy.Input.RepoPathBytes) {
			return app.StreamIdentity{}, "", nil, "", "", &GitError{Code: ErrorLocalCaptureLimit}
		}
		beforeTarget = target
		identity, err = spool.WriteFrom(ctx, relative, strings.NewReader(target))
		if err != nil {
			return app.StreamIdentity{}, "", nil, "", "", classifyCaptureError(err)
		}
	} else {
		file, openErr := os.Open(native)
		if openErr != nil {
			return app.StreamIdentity{}, "", nil, "", "", &GitError{Code: ErrorPermission, Cause: openErr}
		}
		spooled, createErr := spool.CreateFile(ctx, relative)
		if createErr != nil {
			_ = file.Close()
			return app.StreamIdentity{}, "", nil, "", "", createErr
		}
		writer := &captureHashWriter{writer: spooled, hash: sha256.New(), classifier: repository.NewContentClassifierV1(false), text: repository.NewTextByteSemanticsWriter()}
		_, err = io.CopyBuffer(writer, file, make([]byte, 32*1024))
		closeSpoolErr := spooled.Close()
		closeErr := file.Close()
		if err == nil {
			err = closeSpoolErr
		}
		if err == nil {
			err = closeErr
		}
		if err == nil {
			identity = app.StreamIdentity{Bytes: app.ByteSize(writer.bytes), SHA256: hex.EncodeToString(writer.hash.Sum(nil))}
			contentClass = writer.classifier.Classify()
			if contentClass == repository.ContentClassRegularTextUTF8 {
				value, semanticsErr := writer.text.Semantics(uint64(writer.bytes))
				if semanticsErr != nil {
					return app.StreamIdentity{}, "", nil, "", "", semanticsErr
				}
				textSemantics = &value
			}
		}
		if err != nil {
			return app.StreamIdentity{}, "", nil, "", "", classifyCaptureError(err)
		}
	}
	after, statErr := os.Lstat(native)
	if statErr != nil || !sameFileObservation(before, after) || !captureParentsIntact(root, path) {
		if statErr == nil {
			statErr = errors.New("working-tree identity changed")
		}
		return app.StreamIdentity{}, "", nil, "", "", &GitError{Code: ErrorLocalCaptureMutation, Cause: statErr}
	}
	if kind == repository.FileKindSymlink {
		afterTarget, afterErr := os.Readlink(native)
		if afterErr != nil {
			return app.StreamIdentity{}, "", nil, "", "", &GitError{Code: ErrorPermission, Cause: afterErr}
		}
		if beforeTarget == "" || afterTarget != beforeTarget {
			return app.StreamIdentity{}, "", nil, "", "", &GitError{Code: ErrorLocalCaptureMutation, Cause: errors.New("symlink target changed")}
		}
	}
	token := digestParts([]byte(string(path)), []byte(string(kind)), []byte(strconv.FormatUint(uint64(mode), 10)), []byte(identity.SHA256), []byte(strconv.FormatUint(uint64(identity.Bytes), 10)))
	return identity, contentClass, textSemantics, token, relative, nil
}

func (a *LocalCaptureAdapter) revalidateWorkingBlobs(root string, records []captureBlobRecord) error {
	for _, record := range records {
		if record.side != repository.CaptureBlobWorkingTree {
			continue
		}
		kind, mode, err := observeWorkingPath(root, record.path)
		if err != nil {
			return err
		}
		identity, err := hashWorkingPath(root, record.path, kind, mode, a.policy.Artifact.CaptureDeltaBytes)
		if err != nil {
			return err
		}
		if identity.Bytes != record.identity.Bytes || identity.SHA256 != record.identity.SHA256 {
			return &GitError{Code: ErrorLocalCaptureMutation}
		}
	}
	return nil
}

func hashWorkingPath(root string, path repository.RepoPath, expectedKind repository.FileKind, expectedMode uint32, maxBytes app.ByteSize) (app.StreamIdentity, error) {
	native, err := captureNativePath(root, path)
	if err != nil {
		return app.StreamIdentity{}, err
	}
	before, err := os.Lstat(native)
	if err != nil {
		return app.StreamIdentity{}, &GitError{Code: ErrorLocalCaptureMutation, Cause: err}
	}
	kind, mode := fileKindFromNativeInfo(before)
	if kind != expectedKind || mode != expectedMode {
		return app.StreamIdentity{}, &GitError{Code: ErrorLocalCaptureMutation}
	}
	hashValue := sha256.New()
	var bytesRead uint64
	if kind == repository.FileKindSymlink {
		target, readErr := os.Readlink(native)
		if readErr != nil {
			return app.StreamIdentity{}, &GitError{Code: ErrorPermission, Cause: readErr}
		}
		bytesRead = uint64(len(target))
		if bytesRead > uint64(maxBytes) {
			return app.StreamIdentity{}, &GitError{Code: ErrorLocalCaptureLimit}
		}
		_, _ = hashValue.Write([]byte(target))
	} else {
		file, openErr := os.Open(native)
		if openErr != nil {
			return app.StreamIdentity{}, &GitError{Code: ErrorPermission, Cause: openErr}
		}
		buffer := make([]byte, 32*1024)
		for {
			read, readErr := file.Read(buffer)
			if read > 0 {
				bytesRead += uint64(read)
				if bytesRead > uint64(maxBytes) {
					_ = file.Close()
					return app.StreamIdentity{}, &GitError{Code: ErrorLocalCaptureLimit}
				}
				_, _ = hashValue.Write(buffer[:read])
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				_ = file.Close()
				return app.StreamIdentity{}, &GitError{Code: ErrorPermission, Cause: readErr}
			}
		}
		if closeErr := file.Close(); closeErr != nil {
			return app.StreamIdentity{}, &GitError{Code: ErrorPermission, Cause: closeErr}
		}
	}
	after, statErr := os.Lstat(native)
	if statErr != nil || !sameFileObservation(before, after) || !captureParentsIntact(root, path) {
		if statErr == nil {
			statErr = errors.New("working-tree identity changed")
		}
		return app.StreamIdentity{}, &GitError{Code: ErrorLocalCaptureMutation, Cause: statErr}
	}
	return app.StreamIdentity{Bytes: app.ByteSize(bytesRead), SHA256: hex.EncodeToString(hashValue.Sum(nil))}, nil
}

func fileKindFromNativeInfo(info os.FileInfo) (repository.FileKind, uint32) {
	if info == nil {
		return repository.FileKindUnknown, 0
	}
	mode := info.Mode()
	switch {
	case mode.IsRegular():
		gitMode := uint32(0100644)
		if mode.Perm()&0111 != 0 {
			gitMode = 0100755
		}
		return repository.FileKindRegular, gitMode
	case mode&os.ModeSymlink != 0:
		return repository.FileKindSymlink, 0120000
	case mode.IsDir():
		return repository.FileKindDirectory, 0040000
	default:
		return repository.FileKindUnknown, 0
	}
}

func captureNativePath(root string, path repository.RepoPath) (string, error) {
	if err := path.Validate(); err != nil || bytes.IndexByte(path, 0) >= 0 || filepath.IsAbs(string(path)) {
		return "", &GitError{Code: ErrorNativeIdentityUnavailable}
	}
	for _, component := range strings.Split(string(path), "/") {
		if component == "" || component == "." || component == ".." || filepath.Separator == '\\' && strings.ContainsRune(component, '\\') {
			return "", &GitError{Code: ErrorNativeIdentityUnavailable}
		}
	}
	nativeRelative := filepath.FromSlash(string(path))
	joined := filepath.Join(root, nativeRelative)
	relative, err := filepath.Rel(root, joined)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", &GitError{Code: ErrorNativeIdentityUnavailable}
	}
	if !captureParentsIntact(root, path) {
		return "", &GitError{Code: ErrorNativeIdentityUnavailable}
	}
	return joined, nil
}

func captureParentsIntact(root string, path repository.RepoPath) bool {
	parent := root
	components := strings.Split(string(path), "/")
	for _, component := range components[:len(components)-1] {
		parent = filepath.Join(parent, component)
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func attachBlobRefs(entries []captureStatusRecord, records []captureBlobRecord, aggregate app.ArtifactIdentity) []repository.LocalCaptureEntry {
	byPath := make(map[string][]captureBlobRecord)
	for _, record := range records {
		byPath[record.entryKey] = append(byPath[record.entryKey], record)
	}
	result := make([]repository.LocalCaptureEntry, 0, len(entries))
	for _, record := range entries {
		entry := record.entry
		for _, blob := range byPath[captureEntryPath(entry)] {
			if blob.side == repository.CaptureBlobBase {
				entry.Change.OldTextSemantics = blob.textSemantics
			}
			if blob.contentClass != "" && (blob.side == repository.CaptureBlobWorkingTree || blob.side == repository.CaptureBlobIndex || blob.side == repository.CaptureBlobBase) {
				if blob.side == repository.CaptureBlobWorkingTree || blob.side == repository.CaptureBlobIndex || entry.Change.NewPath == nil {
					entry.Change.ContentClass = blob.contentClass
					entry.Change.Binary = blob.contentClass.IsByteOriented()
					if blob.side != repository.CaptureBlobBase {
						entry.Change.NewTextSemantics = blob.textSemantics
					}
				}
			}
			entry.Blobs = append(entry.Blobs, repository.CaptureBlobRef{
				Side:          blob.side,
				Path:          blob.path,
				ContentClass:  blob.contentClass,
				TextSemantics: blob.textSemantics,
				Artifact: repository.CaptureArtifact{
					Kind:          repository.CaptureArtifactBlobs,
					SpoolID:       aggregate.SpoolID,
					ManifestHash:  aggregate.ManifestHash,
					RelativePath:  blob.relative,
					Bytes:         uint64(blob.identity.Bytes),
					Entries:       1,
					ContentSHA256: blob.identity.SHA256,
					VerifiedAt:    aggregate.VerifiedAt,
				},
			})
		}
		sort.SliceStable(entry.Blobs, func(i, j int) bool { return entry.Blobs[i].Side < entry.Blobs[j].Side })
		result = append(result, entry)
	}
	return result
}

func captureEntryPath(entry repository.LocalCaptureEntry) string {
	if entry.Change.NewPath != nil {
		return string(*entry.Change.NewPath)
	}
	if entry.Change.OldPath != nil {
		return string(*entry.Change.OldPath)
	}
	return ""
}

type captureHashWriter struct {
	writer io.Writer
	hash   interface {
		Write([]byte) (int, error)
		Sum([]byte) []byte
	}
	bytes      int64
	classifier *repository.ContentClassifierV1
	text       *repository.TextByteSemanticsWriter
}

func (w *captureHashWriter) Write(value []byte) (int, error) {
	if w == nil || w.writer == nil || w.hash == nil {
		return 0, io.ErrClosedPipe
	}
	written, err := w.writer.Write(value)
	if written > 0 {
		if _, hashErr := w.hash.Write(value[:written]); hashErr != nil && err == nil {
			err = hashErr
		}
		if int64(written) > int64(^uint64(0)>>1)-w.bytes && err == nil {
			err = errors.New("capture stream byte overflow")
		} else {
			w.bytes += int64(written)
		}
		if w.classifier != nil {
			_, _ = w.classifier.Write(value[:written])
		}
		if w.text != nil {
			if _, textErr := w.text.Write(value[:written]); textErr != nil && err == nil {
				err = textErr
			}
		}
	}
	if err == nil && written != len(value) {
		err = io.ErrShortWrite
	}
	return written, err
}

func acceptNoIndexDifference(err error) bool {
	var gitErr *GitError
	return errors.As(err, &gitErr) && gitErr.Code == ErrorCommandFailed && gitErr.ExitCode == 1
}

func classifyCaptureError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, app.ErrSpoolLimitExceeded) {
		return &GitError{Code: ErrorLocalCaptureLimit, Cause: err}
	}
	var gitErr *GitError
	if errors.As(err, &gitErr) && gitErr.Code == ErrorOutputLimit {
		return &GitError{Code: ErrorLocalCaptureLimit, Cause: err, ExitCode: gitErr.ExitCode, Stderr: gitErr.Stderr}
	}
	return err
}
