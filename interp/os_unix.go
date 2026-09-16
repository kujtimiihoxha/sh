// Copyright (c) 2017, Andrey Nering <andrey.nering@gmail.com>
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"context"
	"errors"
	"os"
	"os/user"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
	"mvdan.cc/sh/v3/syntax"
)

func mkfifo(path string, mode uint32) error {
	return unix.Mkfifo(path, mode)
}

// A FIFO open blocks until its peer opens the other end. On cancellation,
// hold both ends open until the pending open returns, then close all handles.
func openFIFO(ctx context.Context, path string, flags int) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	opened := make(chan struct{})
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		peer, _ := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
		<-opened
		if peer != nil {
			peer.Close()
		}
		close(stopped)
	})
	f, err := os.OpenFile(path, flags, 0)
	close(opened)
	if !stop() {
		<-stopped
	}
	if ctx.Err() != nil {
		if f != nil {
			f.Close()
		}
		return nil, ctx.Err()
	}
	return f, err
}

// defaultAccess is similar to checking the permission bits from [io/fs.FileInfo],
// but it also takes into account the current user's role.
func defaultAccess(ctx context.Context, path string, mode AccessMode) error {
	return unix.Access(path, uint32(mode))
}

// unTestOwnOrGrp implements the -O and -G unary tests. If the file does not
// exist, or the current user cannot be retrieved, returns false.
func (r *Runner) unTestOwnOrGrp(ctx context.Context, op syntax.UnTestOperator, x string) bool {
	info, err := r.stat(ctx, x)
	if err != nil {
		return false
	}
	u, err := user.Current()
	if err != nil {
		return false
	}
	if op == syntax.TsUsrOwn {
		uid, _ := strconv.Atoi(u.Uid)
		return uint32(uid) == info.Sys().(*syscall.Stat_t).Uid
	}
	gid, _ := strconv.Atoi(u.Gid)
	return uint32(gid) == info.Sys().(*syscall.Stat_t).Gid
}

type waitStatus = syscall.WaitStatus

// isENOEXEC reports whether the kernel refused to execute a file
// with ENOEXEC, e.g. a script without a shebang line.
func isENOEXEC(err error) bool { return errors.Is(err, syscall.ENOEXEC) }

// isETXTBSY reports whether the kernel refused to execute a file
// with ETXTBSY, i.e. a process holds it open for writing.
func isETXTBSY(err error) bool { return errors.Is(err, syscall.ETXTBSY) }
