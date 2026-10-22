package protocol

import "errors"

// ErrCommandNotStarted marks a command runner failure that the host proved happened before the command
// started: the cell never received its start acknowledgement, and its processes are observed gone. No
// command output exists and the command's effect cannot have happened.
var ErrCommandNotStarted = errors.New("command cell failed before its command started; cell settled")
