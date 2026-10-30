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

func Reopen(*os.File, ReopenApproval) (*VerifiedRepository, error) {
	return nil, ErrUnsupportedPlatform
}

func repositoryIdentity(*os.File, string, Provenance, Snapshot) (RepositoryIdentity, error) {
	return RepositoryIdentity{}, ErrUnsupportedPlatform
}

func verifyRepositoryLocked(*Repository) error { return ErrUnsupportedPlatform }

func destroyRepositoryLocked(*Repository) error { return ErrUnsupportedPlatform }
