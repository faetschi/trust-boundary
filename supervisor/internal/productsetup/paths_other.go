//go:build !linux

package productsetup

import "os"

func directoryNotEmpty(error) bool { return false }

func (h *host) openRoot() error                                 { return refuse("product host setup requires Linux") }
func (h *host) withPath(string, bool, func(string) error) error { return h.openRoot() }
func (h *host) chmod(string, os.FileMode) error                 { return h.openRoot() }
func (h *host) appendLine(string, string) error                 { return h.openRoot() }
