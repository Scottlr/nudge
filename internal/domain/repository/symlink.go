package repository

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const (
	SymlinkPolicyVersion    = "v1"
	SymlinkPrimitiveVersion = "v1"
	SymlinkInlinePreviewMax = 4 * 1024
	SymlinkRangeMax         = 64 * 1024
	SymlinkNativeActionMax  = 32 * 1024
	SymlinkMaxComponents    = 256
	SymlinkMaxDepth         = 128
)

var ErrInvalidSymlinkEvidence = errors.New("invalid symlink evidence")

// SymlinkTargetClass is the lexical, referent-independent safety class of a
// link target. Classifying a target never reads or follows its referent.
type SymlinkTargetClass string

const (
	SymlinkRelativeContained  SymlinkTargetClass = "relative_contained"
	SymlinkAbsolute           SymlinkTargetClass = "absolute"
	SymlinkLexicallyEscaping  SymlinkTargetClass = "lexically_escaping"
	SymlinkGitAdminAlias      SymlinkTargetClass = "git_admin_alias"
	SymlinkUnrepresentable    SymlinkTargetClass = "unrepresentable"
	SymlinkContainmentUnknown SymlinkTargetClass = "containment_unknown"
)

func (c SymlinkTargetClass) Validate() error {
	switch c {
	case SymlinkRelativeContained, SymlinkAbsolute, SymlinkLexicallyEscaping, SymlinkGitAdminAlias, SymlinkUnrepresentable, SymlinkContainmentUnknown:
		return nil
	default:
		return ErrInvalidSymlinkEvidence
	}
}

// SymlinkTargetRef is the bounded immutable identity used for target display
// and ranges. InlinePreview is only a prefix; it is never the target truth.
type SymlinkTargetRef struct {
	TargetHash    string
	Length        uint64
	InlinePreview []byte
}

func NewSymlinkTargetRef(target []byte) SymlinkTargetRef {
	digest := sha256.Sum256(target)
	preview := target
	if len(preview) > SymlinkInlinePreviewMax {
		preview = preview[:SymlinkInlinePreviewMax]
	}
	return SymlinkTargetRef{TargetHash: hex.EncodeToString(digest[:]), Length: uint64(len(target)), InlinePreview: append([]byte(nil), preview...)}
}

func (r SymlinkTargetRef) Validate() error {
	if !validContentHash(r.TargetHash) || uint64(len(r.InlinePreview)) > r.Length || len(r.InlinePreview) > SymlinkInlinePreviewMax {
		return ErrInvalidSymlinkEvidence
	}
	return nil
}

// ClassifySymlinkTarget performs bounded lexical reduction from a link path's
// parent. It deliberately does not call EvalSymlinks or inspect the target.
func ClassifySymlinkTarget(linkPath RepoPath, target []byte, maxComponents, maxDepth int) SymlinkTargetClass {
	if linkPath.Validate() != nil {
		return SymlinkContainmentUnknown
	}
	if maxComponents <= 0 {
		maxComponents = SymlinkMaxComponents
	}
	if maxDepth <= 0 {
		maxDepth = SymlinkMaxDepth
	}
	if len(target) == 0 || bytes.IndexByte(target, 0) >= 0 {
		return SymlinkUnrepresentable
	}
	if target[0] == '/' || target[0] == '\\' || len(target) > 1 && target[1] == ':' {
		return SymlinkAbsolute
	}
	if bytes.IndexByte(target, '\\') >= 0 {
		return SymlinkUnrepresentable
	}
	parent := bytes.Split(linkPath.Bytes(), []byte{'/'})
	if len(parent) == 0 {
		return SymlinkContainmentUnknown
	}
	stack := append([][]byte(nil), parent[:len(parent)-1]...)
	for _, component := range stack {
		if bytes.EqualFold(component, []byte(".git")) {
			return SymlinkGitAdminAlias
		}
	}
	parts := bytes.Split(target, []byte{'/'})
	if len(parts) > maxComponents {
		return SymlinkUnrepresentable
	}
	for _, component := range parts {
		if len(component) == 0 {
			return SymlinkUnrepresentable
		}
		if bytes.EqualFold(component, []byte(".git")) {
			return SymlinkGitAdminAlias
		}
		switch {
		case bytes.Equal(component, []byte(".")):
			continue
		case bytes.Equal(component, []byte("..")):
			if len(stack) == 0 {
				return SymlinkLexicallyEscaping
			}
			stack = stack[:len(stack)-1]
		default:
			stack = append(stack, component)
		}
		if len(stack) > maxDepth {
			return SymlinkUnrepresentable
		}
	}
	return SymlinkRelativeContained
}

// SymlinkEvidence binds target identity to one root-bound parent observation
// and the exact no-follow primitive that qualified it.
type SymlinkEvidence struct {
	PathKey          RepoPathKey
	TargetHash       string
	TargetLength     uint64
	TargetClass      SymlinkTargetClass
	RootIdentity     string
	ParentChainHash  string
	Platform         string
	PrimitiveVersion string
	ReasonCode       string
}

func NewSymlinkEvidence(path RepoPath, target []byte, rootIdentity, parentChainHash, platform, primitiveVersion string) (SymlinkEvidence, error) {
	if path.Validate() != nil || !validText(rootIdentity) || !validText(parentChainHash) || !validText(platform) || !validText(primitiveVersion) {
		return SymlinkEvidence{}, ErrInvalidSymlinkEvidence
	}
	ref := NewSymlinkTargetRef(target)
	class := ClassifySymlinkTarget(path, target, SymlinkMaxComponents, SymlinkMaxDepth)
	evidence := SymlinkEvidence{PathKey: path.Key(), TargetHash: ref.TargetHash, TargetLength: ref.Length, TargetClass: class, RootIdentity: rootIdentity, ParentChainHash: parentChainHash, Platform: platform, PrimitiveVersion: primitiveVersion, ReasonCode: symlinkReason(class)}
	if err := evidence.Validate(); err != nil {
		return SymlinkEvidence{}, err
	}
	return evidence, nil
}

func (e SymlinkEvidence) Validate() error {
	if _, err := e.PathKey.Path(); err != nil || !validContentHash(e.TargetHash) || e.TargetClass.Validate() != nil || !validText(e.RootIdentity) || !validText(e.ParentChainHash) || !validText(e.Platform) || !validText(e.PrimitiveVersion) {
		return ErrInvalidSymlinkEvidence
	}
	if e.TargetClass == SymlinkRelativeContained {
		if e.ReasonCode != "" {
			return ErrInvalidSymlinkEvidence
		}
	} else if !validSymlinkReason(e.ReasonCode) {
		return ErrInvalidSymlinkEvidence
	}
	return nil
}

func (e SymlinkEvidence) IsActionable() bool {
	return e.Validate() == nil && e.TargetClass == SymlinkRelativeContained && e.PrimitiveVersion == SymlinkPrimitiveVersion && e.ReasonCode == ""
}

func symlinkReason(class SymlinkTargetClass) string {
	switch class {
	case SymlinkAbsolute:
		return "symlink_absolute"
	case SymlinkLexicallyEscaping:
		return "symlink_lexically_escaping"
	case SymlinkGitAdminAlias:
		return "symlink_git_admin_alias"
	case SymlinkUnrepresentable:
		return "symlink_unrepresentable"
	case SymlinkContainmentUnknown:
		return "symlink_containment_unknown"
	default:
		return ""
	}
}

func validSymlinkReason(value string) bool {
	switch value {
	case "symlink_absolute", "symlink_lexically_escaping", "symlink_git_admin_alias", "symlink_unrepresentable", "symlink_containment_unknown", "symlink_platform_unavailable", "symlink_reparse_unsupported", "symlink_stale":
		return true
	default:
		return false
	}
}
