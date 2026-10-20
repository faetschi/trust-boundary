//go:build !linux

package main

import (
	"errors"
	"io"
)

func runServe(args []string, transcript io.Writer, stderr io.Writer) error {
	return errors.New("native Pi host profile is missing; production --pi launch is refused")
}

func runAbandonUnknown(args []string, stdout io.Writer, stderr io.Writer) error {
	return errors.New("abandon-unknown is available only on Linux")
}

func runWorkflowRootInit(args []string, stdout io.Writer, stderr io.Writer) error {
	return errors.New("workflow-root-init is available only on Linux")
}
