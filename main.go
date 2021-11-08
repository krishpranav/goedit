package main

import (
	"errors"
	"fmt"
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

	origTermios *unix.Termios
}

type Row struct {
	idx int

	chars []rune

	render string

	hl []uint8

	hasUnclosedComment bool
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

func (e *Editor) Init() error {
	termios, err := enableRawMode()
	if err != nil {
		return err
	}
	e.origTermios = termios
	ws, err := unix.IoctlGetWinsize(stdoutfd, unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 {

		if _, err = os.Stdout.Write([]byte("\x1b[999C\x1b[999B")); err != nil {
			return err
		}
		if row, col, err := getCursorPosition(); err == nil {
			e.screenRows = row
			e.screenCols = col
			return nil
		}
		return err
	}
	e.screenRows = int(ws.Row) - 2
	e.screenCols = int(ws.Col)
	return nil
}

func getCursorPosition() (row, col int, err error) {
	if _, err = os.Stdout.Write([]byte("\x1b[6n")); err != nil {
		return
	}
	if _, err = fmt.Fscanf(os.Stdin, "\x1b[%d;%d", &row, &col); err != nil {
		return
	}
	return
}
