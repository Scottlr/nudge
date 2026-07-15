package paths

import (
	"io/fs"

	"github.com/Scottlr/nudge/internal/domain/repository"
)

// ClassifySpecialPath returns a no-follow special-entry classification. It
// never opens an entry for content and treats an unclassifiable native entry
// as bounded review-only evidence.
func ClassifySpecialPath(path string, mode fs.FileMode) (repository.SpecialFileKind, bool) {
	if kind, ok := repository.SpecialFileKindFromMode(mode); ok {
		return kind, true
	}
	return classifyReparsePoint(path)
}
