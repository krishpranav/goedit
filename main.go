package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var version = "1.0"

const tabstop = 8

var (
	stdinfd  = int(os.Stdin.Fd())
	stdoutfd = int(os.Stdout.Fd())
)

var ErrQuitEditor = errors.New("quit editor")

type Editor struct {
	cx, cy     int
	rx         int
	rowOffset  int
	colOffset  int
	screenRows int
	screenCols int

	rows []*Row

	dirty int

	quitCounter int

	filename string
}

func enableRawMode() (*unix.Termios, error) {
	t, err := unix.IoctlGetTermios(stdinfd, ioctlReadTermios)
	if err != nil {
		return nil, err
	}
	raw := *t
	raw.Iflag &^= unix.BRKINT | unix.INPCK | unix.ISTRIP | unix.IXON

	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(stdinfd, ioctlWriteTermios, &raw); err != nil {
		return nil, err
	}
	return t, nil
}
