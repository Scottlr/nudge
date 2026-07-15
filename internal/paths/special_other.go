//go:build !windows

package paths

import "github.com/Scottlr/nudge/internal/domain/repository"

func classifyReparsePoint(string) (repository.SpecialFileKind, bool) {
	return "", false
}
