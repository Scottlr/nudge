//go:build !linux && !darwin && !windows

package artifactspool

import "github.com/Scottlr/nudge/internal/app"

func renameNoReplace(string, string) error { return app.ErrSpoolPublicationUnsupported }
