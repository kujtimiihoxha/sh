// Copyright (c) 2026, the contributors
// See LICENSE for licensing information.

package interp

import (
	"io"
	"slices"
	"sync"
)

// sharedFile keeps a redirection open until every shell that inherited it exits.
type sharedFile struct {
	mu     sync.Mutex
	closer io.Closer
	refs   int
}

func (f *sharedFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs--
	if f.refs == 0 {
		return f.closer.Close()
	}
	return nil
}

// Retain before starting a goroutine: the parent can close its scope immediately.
func (r *Runner) retainFiles() func() {
	files := slices.Clone(r.files)
	for _, f := range files {
		f.mu.Lock()
		f.refs++
		f.mu.Unlock()
	}
	return func() {
		for _, f := range files {
			f.Close()
		}
	}
}
