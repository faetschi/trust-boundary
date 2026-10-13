package piruntime

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

// ownedStdoutPipe keeps the parent reader and writer ends under explicit
// ownership. The command receives the underlying writer file directly.
type ownedStdoutPipe struct {
	cmd    *exec.Cmd
	reader *ownedStdoutReader
	writer *ownedStdoutWriter
}

type ownedStdoutReader struct {
	file     *os.File
	close    sync.Once
	closeErr error
}

func (r *ownedStdoutReader) Read(p []byte) (int, error) {
	if r == nil || r.file == nil {
		return 0, os.ErrInvalid
	}
	return r.file.Read(p)
}

func (r *ownedStdoutReader) Close() error {
	if r == nil {
		return nil
	}
	r.close.Do(func() {
		if r.file != nil {
			r.closeErr = r.file.Close()
		}
	})
	return r.closeErr
}

type ownedStdoutWriter struct {
	file     *os.File
	close    sync.Once
	closeErr error
}

func (w *ownedStdoutWriter) Close() error {
	if w == nil {
		return nil
	}
	w.close.Do(func() {
		if w.file != nil {
			w.closeErr = w.file.Close()
		}
	})
	return w.closeErr
}

// attachOwnedStdout attaches an os.Pipe writer to this exact command. The
// parent retains both ends until Start closes its writer or reports failure.
func attachOwnedStdout(cmd *exec.Cmd) (*ownedStdoutPipe, error) {
	if cmd == nil {
		return nil, errors.New("cannot attach stdout to a nil command")
	}
	readerFile, writerFile, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	pipe := &ownedStdoutPipe{
		cmd:    cmd,
		reader: &ownedStdoutReader{file: readerFile},
		writer: &ownedStdoutWriter{file: writerFile},
	}
	cmd.Stdout = writerFile
	return pipe, nil
}

// Start starts the attached command and closes the parent's writer exactly
// once. On a failed start it closes both parent-owned ends.
func (p *ownedStdoutPipe) Start() error {
	if p == nil || p.cmd == nil {
		return errors.New("owned stdout command is unavailable")
	}
	if err := p.cmd.Start(); err != nil {
		return errors.Join(err, p.writer.Close(), p.reader.Close())
	}
	return p.writer.Close()
}

func (p *ownedStdoutPipe) CloseWriter() error {
	if p == nil {
		return nil
	}
	return p.writer.Close()
}

func (p *ownedStdoutPipe) CloseReader() error {
	if p == nil {
		return nil
	}
	return p.reader.Close()
}

var _ io.ReadCloser = (*ownedStdoutReader)(nil)
