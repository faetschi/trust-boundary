//go:build !linux

package webview

import "errors"

func reconstructGenerationProjection([]byte) (GenerationProjection, error) {
	return GenerationProjection{}, errors.New("session-repository evidence reconstruction is available only on Linux")
}
