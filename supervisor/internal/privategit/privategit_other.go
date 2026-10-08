//go:build !linux

package privategit

import (
	"context"
	"os"
)

func PinGitExecutable(string, GitExecutableApproval) (PinnedGit, error) {
	return PinnedGit{}, ErrUnsupportedPlatform
}

func Create(context.Context, *os.File, SourceDescriptor, Options) (*Repository, error) {
	return nil, ErrUnsupportedPlatform
}

func verifyRepositoryLocked(*Repository) error { return ErrUnsupportedPlatform }

func destroyRepositoryLocked(*Repository) error { return ErrUnsupportedPlatform }
