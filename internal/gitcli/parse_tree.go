package gitcli

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

type treeRecord struct {
	Path     repository.RepoPath
	Kind     repository.FileKind
	Mode     uint32
	ObjectID *repository.ObjectID
}

func parseTreeRecord(record []byte) (treeRecord, error) {
	separator := bytes.IndexByte(record, '\t')
	if separator <= 0 || separator == len(record)-1 {
		return treeRecord{}, malformedTree("tree record separator")
	}
	fields := strings.Fields(string(record[:separator]))
	if len(fields) != 3 {
		return treeRecord{}, malformedTree("tree record header")
	}
	mode, err := parseTreeMode(fields[0])
	if err != nil {
		return treeRecord{}, err
	}
	path, err := repository.NewRepoPath(record[separator+1:])
	if err != nil {
		return treeRecord{}, malformedTree("tree record path")
	}
	kind := fileKindFromGitMode(mode)
	if fields[1] == "tree" {
		kind = repository.FileKindDirectory
	} else if fields[1] == "blob" && kind == repository.FileKindUnknown {
		return treeRecord{}, malformedTree("unknown blob mode")
	} else if fields[1] != "blob" && fields[1] != "commit" {
		return treeRecord{}, malformedTree("unknown tree record type")
	}
	objectID, err := parseTreeObject(fields[2])
	if err != nil {
		return treeRecord{}, err
	}
	return treeRecord{Path: path, Kind: kind, Mode: mode, ObjectID: objectID}, nil
}

func parseTreeMode(value string) (uint32, error) {
	if value == "" {
		return 0, malformedTree("empty tree mode")
	}
	parsed, err := strconv.ParseUint(value, 8, 32)
	if err != nil || parsed == 0 || repository.ValidateGitMode(uint32(parsed)) != nil {
		return 0, malformedTree("invalid tree mode")
	}
	return uint32(parsed), nil
}

func parseTreeObject(value string) (*repository.ObjectID, error) {
	if value == "" || allZeroText(value) {
		return nil, nil
	}
	id, err := repository.NewObjectID(value)
	if err != nil {
		return nil, malformedTree("invalid tree object")
	}
	return &id, nil
}

type lsFilesRecord struct {
	Path     repository.RepoPath
	Mode     uint32
	Stage    uint8
	ObjectID *repository.ObjectID
}

func parseLSFilesRecord(record []byte) (lsFilesRecord, error) {
	separator := bytes.IndexByte(record, '\t')
	if separator < 0 {
		path, err := repository.NewRepoPath(record)
		if err != nil {
			return lsFilesRecord{}, malformedTree("untracked path")
		}
		return lsFilesRecord{Path: path}, nil
	}
	path, err := repository.NewRepoPath(record[separator+1:])
	if err != nil {
		return lsFilesRecord{}, malformedTree("index path")
	}
	header := record[:separator]
	if len(header) == 0 {
		return lsFilesRecord{Path: path}, nil
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 {
		return lsFilesRecord{}, malformedTree("index record header")
	}
	mode, err := parseTreeMode(fields[0])
	if err != nil {
		return lsFilesRecord{}, err
	}
	stage, err := strconv.ParseUint(fields[2], 10, 8)
	if err != nil || stage > 3 {
		return lsFilesRecord{}, malformedTree("index stage")
	}
	objectID, err := parseTreeObject(fields[1])
	if err != nil {
		return lsFilesRecord{}, err
	}
	return lsFilesRecord{Path: path, Mode: mode, Stage: uint8(stage), ObjectID: objectID}, nil
}

func parseRawDiff(data []byte) ([]repository.ChangedFile, error) {
	parts := bytes.Split(data, []byte{0})
	changes := make([]repository.ChangedFile, 0, len(parts))
	for index := 0; index < len(parts); index++ {
		record := parts[index]
		if len(record) == 0 {
			continue
		}
		separator := bytes.IndexByte(record, '\t')
		if separator == 0 {
			return nil, malformedTree("raw diff record")
		}
		header := record
		var oldPathBytes []byte
		if separator > 0 {
			header = record[:separator]
			oldPathBytes = record[separator+1:]
		} else {
			if index+1 >= len(parts) || len(parts[index+1]) == 0 {
				return nil, malformedTree("raw diff path")
			}
			oldPathBytes = parts[index+1]
			index++
		}
		fields := strings.Fields(string(header))
		if len(fields) != 5 || len(fields[0]) < 2 {
			return nil, malformedTree("raw diff header")
		}
		oldMode, err := parseTreeModeAllowZero(fields[0][1:])
		if err != nil {
			return nil, err
		}
		newMode, err := parseTreeModeAllowZero(fields[1])
		if err != nil {
			return nil, err
		}
		oldID, err := parseTreeObject(fields[2])
		if err != nil {
			return nil, err
		}
		newID, err := parseTreeObject(fields[3])
		if err != nil {
			return nil, err
		}
		status := fields[4]
		if status == "" {
			return nil, malformedTree("empty raw diff status")
		}
		oldPathValue, err := repository.NewRepoPath(oldPathBytes)
		if err != nil {
			return nil, malformedTree("raw diff path")
		}
		newPathValue := oldPathValue
		var oldPath, newPath *repository.RepoPath
		oldPath = &oldPathValue
		newPath = &newPathValue
		kind := repository.ChangeModified
		switch status[0] {
		case 'A':
			oldPath = nil
			kind = repository.ChangeAdded
		case 'D':
			newPath = nil
			kind = repository.ChangeDeleted
		case 'R', 'C':
			if index+1 >= len(parts) || len(parts[index+1]) == 0 {
				return nil, malformedTree("raw rename path")
			}
			newPathValue, err = repository.NewRepoPath(parts[index+1])
			if err != nil {
				return nil, malformedTree("raw rename destination")
			}
			newPath = &newPathValue
			index++
			if status[0] == 'R' {
				kind = repository.ChangeRenamed
			} else {
				kind = repository.ChangeCopied
			}
		case 'T':
			kind = repository.ChangeTypeChanged
		case 'M', 'U':
			kind = repository.ChangeModified
		default:
			return nil, malformedTree("unsupported raw diff status")
		}
		change := repository.ChangedFile{
			OldPath: oldPath, NewPath: newPath, Kind: kind,
			OldFileKind: fileKindFromGitMode(oldMode), NewFileKind: fileKindFromGitMode(newMode),
			OldMode: oldMode, NewMode: newMode, OldObjectID: oldID, NewObjectID: newID,
		}
		if oldPath != nil && newPath != nil && (oldMode != newMode || change.OldFileKind != change.NewFileKind) {
			transition, transitionErr := repository.NewModeTransition(oldMode, newMode)
			if transitionErr != nil {
				return nil, transitionErr
			}
			if transition.Kind == repository.ModeTypeChanged && (kind == repository.ChangeRenamed || kind == repository.ChangeCopied) {
				return nil, malformedTree("rename type transition")
			}
			change.ModeTransition = &transition
			if transition.Kind == repository.ModeTypeChanged {
				change.Kind = repository.ChangeTypeChanged
			}
		}
		if oldPath == nil {
			change.OldFileKind, change.OldMode, change.OldObjectID = "", 0, nil
		}
		if newPath == nil {
			change.NewFileKind, change.NewMode, change.NewObjectID = "", 0, nil
		}
		if kind == repository.ChangeRenamed || kind == repository.ChangeCopied {
			similarity, scoreErr := parseRenameScore([]byte(status[1:]))
			if scoreErr != nil {
				return nil, scoreErr
			}
			evidence, evidenceErr := repository.NewRenameEvidence(1, similarity, kind, *change.OldPath, *change.NewPath)
			if evidenceErr != nil {
				return nil, malformedTree("rename evidence")
			}
			change.Rename = &evidence
		}
		if err := change.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", errInvalidTreeOutput, err)
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func parseTreeModeAllowZero(value string) (uint32, error) {
	if value == "000000" || value == "0" {
		return 0, nil
	}
	return parseTreeMode(value)
}

func treeEntryForRecord(record treeRecord, parent *repository.RepoPath, changed *repository.ChangedFile) (repository.TreeEntry, bool, error) {
	child, direct, syntheticDirectory := treeChildPath(record.Path, parent)
	if !direct {
		return repository.TreeEntry{}, false, nil
	}
	kind, mode, objectID := record.Kind, record.Mode, record.ObjectID
	if syntheticDirectory && kind != repository.FileKindDirectory {
		kind, mode, objectID = repository.FileKindDirectory, 0o40000, nil
		changed = nil
	}
	entry, err := newTreeEntry(child, kind, mode, objectID, changed)
	if err != nil {
		return repository.TreeEntry{}, false, err
	}
	return entry, true, nil
}

func newTreeEntry(path repository.RepoPath, kind repository.FileKind, mode uint32, objectID *repository.ObjectID, changed *repository.ChangedFile) (repository.TreeEntry, error) {
	if path.Validate() != nil {
		return repository.TreeEntry{}, errInvalidTreeOutput
	}
	lastSlash := bytes.LastIndexByte(path, '/')
	var parent repository.RepoPath
	name := path
	if lastSlash >= 0 {
		parent = repository.RepoPath(path[:lastSlash])
		name = repository.RepoPath(path[lastSlash+1:])
	}
	entry := repository.TreeEntry{
		Path: path.Bytes(), Name: name.Bytes(), Parent: parent.Bytes(), Kind: kind, Mode: mode, ModeClass: repository.ClassifyGitMode(mode),
		ObjectID: cloneObjectID(objectID), LazyChild: kind == repository.FileKindDirectory,
		ChangedSummary: cloneChangedFile(changed),
	}
	if changed != nil && changed.ReviewOnly != nil {
		entry.ReviewOnly = cloneReviewOnly(changed.ReviewOnly)
	}
	if err := entry.Validate(); err != nil {
		return repository.TreeEntry{}, fmt.Errorf("%w: %v", errInvalidTreeOutput, err)
	}
	return entry, nil
}

func treeChildPath(path repository.RepoPath, parent *repository.RepoPath) (repository.RepoPath, bool, bool) {
	remaining := path
	prefix := repository.RepoPath(nil)
	if parent != nil {
		if bytes.Equal(path, *parent) {
			return nil, false, false
		}
		prefix = append(prefix, parent.Bytes()...)
		prefix = append(prefix, '/')
		if !bytes.HasPrefix(path, prefix) {
			return nil, false, false
		}
		remaining = path[len(prefix):]
	}
	if len(remaining) == 0 {
		return nil, false, false
	}
	if slash := bytes.IndexByte(remaining, '/'); slash >= 0 {
		child := append(append(repository.RepoPath(nil), prefix...), remaining[:slash]...)
		return child, true, true
	}
	return append(append(repository.RepoPath(nil), prefix...), remaining...), true, false
}

func cloneObjectID(value *repository.ObjectID) *repository.ObjectID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneReviewOnly(value *repository.ReviewOnlyEntryEvidence) *repository.ReviewOnlyEntryEvidence {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneChangedFile(value *repository.ChangedFile) *repository.ChangedFile {
	if value == nil {
		return nil
	}
	copyValue := *value
	copyValue.OldPath = cloneRepoPath(value.OldPath)
	copyValue.NewPath = cloneRepoPath(value.NewPath)
	copyValue.OldObjectID = cloneObjectID(value.OldObjectID)
	copyValue.NewObjectID = cloneObjectID(value.NewObjectID)
	if value.Conflict != nil {
		conflict := *value.Conflict
		conflict.Stage1 = cloneIndexStage(value.Conflict.Stage1)
		conflict.Stage2 = cloneIndexStage(value.Conflict.Stage2)
		conflict.Stage3 = cloneIndexStage(value.Conflict.Stage3)
		copyValue.Conflict = &conflict
	}
	if value.Rename != nil {
		rename := *value.Rename
		copyValue.Rename = &rename
	}
	if value.ReviewOnly != nil {
		reviewOnly := *value.ReviewOnly
		copyValue.ReviewOnly = &reviewOnly
	}
	if value.OldTextSemantics != nil {
		semantics := *value.OldTextSemantics
		copyValue.OldTextSemantics = &semantics
	}
	if value.NewTextSemantics != nil {
		semantics := *value.NewTextSemantics
		copyValue.NewTextSemantics = &semantics
	}
	if value.ModeTransition != nil {
		transition := *value.ModeTransition
		copyValue.ModeTransition = &transition
	}
	return &copyValue
}

func cloneGitTreeEntry(value repository.TreeEntry) repository.TreeEntry {
	value.Path = repository.RepoPath(value.Path.Bytes())
	value.Name = repository.RepoPath(value.Name.Bytes())
	value.Parent = repository.RepoPath(value.Parent.Bytes())
	value.ObjectID = cloneObjectID(value.ObjectID)
	if value.ReviewOnly != nil {
		reviewOnly := *value.ReviewOnly
		value.ReviewOnly = &reviewOnly
	}
	value.ChangedSummary = cloneChangedFile(value.ChangedSummary)
	return value
}

func cloneRepoPath(value *repository.RepoPath) *repository.RepoPath {
	if value == nil {
		return nil
	}
	copyValue := repository.RepoPath(value.Bytes())
	return &copyValue
}

func cloneIndexStage(value *repository.IndexStage) *repository.IndexStage {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
