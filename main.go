/* making this file exec */
package main

/* imports */
import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
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
	cx, cy int
	rx     int

	rowOffset int
	colOffset int

	screenRows int
	screenCols int

	rows []*Row

	dirty int

	quitCounter int

	filename string

	statusmsg     string
	statusmsgTime time.Time

	syntax *EditorSyntax

	origTermios *unix.Termios
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

func (e *Editor) Close() error {
	if e.origTermios == nil {
		return fmt.Errorf("raw mode is not enabled")
	}

	return unix.IoctlSetTermios(stdinfd, ioctlWriteTermios, e.origTermios)
}

type key int32

const (
	keyEnter     key = 10
	keyBackspace key = 127

	keyArrowLeft key = iota + 1000
	keyArrowRight
	keyArrowUp
	keyArrowDown
	keyDelete
	keyPageUp
	keyPageDown
	keyHome
	keyEnd
)

const (
	hlNormal uint8 = iota
	hlComment
	hlMlComment
	hlKeyword1
	hlKeyword2
	hlString
	hlNumber
	hlMatch
)

const (
	HL_HIGHLIGHT_NUMBERS = 1 << iota
	HL_HIGHLIGHT_STRINGS
)

type EditorSyntax struct {
	filetype string

	filematch []string

	keywords []string

	scs string

	mcs string

	mce string

	flags int
}

var HLDB = []*EditorSyntax{
	{
		filetype:  "c",
		filematch: []string{".c", ".h", "cpp", ".cc"},
		keywords: []string{
			"switch", "if", "while", "for", "break", "continue", "return",
			"else", "struct", "union", "typedef", "static", "enum", "class",
			"case",

			"int|", "long|", "double|", "float|", "char|", "unsigned|",
			"signed|", "void|",
		},
		scs:   "//",
		mcs:   "/*",
		mce:   "*/",
		flags: HL_HIGHLIGHT_NUMBERS | HL_HIGHLIGHT_STRINGS,
	},
	{
		filetype:  "go",
		filematch: []string{".go"},
		keywords: []string{
			"break", "default", "func", "interface", "select", "case", "defer",
			"go", "map", "struct", "chan", "else", "goto", "package", "switch",
			"const", "fallthrough", "if", "range", "type", "continue", "for",
			"import", "return", "var",

			"append|", "bool|", "byte|", "cap|", "close|", "complex|",
			"complex64|", "complex128|", "error|", "uint16|", "copy|", "false|",
			"float32|", "float64|", "imag|", "int|", "int8|", "int16|",
			"uint32|", "int32|", "int64|", "iota|", "len|", "make|", "new|",
			"nil|", "panic|", "uint64|", "print|", "println|", "real|",
			"recover|", "rune|", "string|", "true|", "uint|", "uint8|",
			"uintptr|",
		},
		scs:   "//",
		mcs:   "/*",
		mce:   "*/",
		flags: HL_HIGHLIGHT_NUMBERS | HL_HIGHLIGHT_STRINGS,
	},
}

type Row struct {
	idx int

	chars []rune

	render string

	hl []uint8

	hasUnclosedComment bool
}

func ctrl(char byte) byte {
	return char & 0x1f
}

func die(err error) {
	os.Stdout.WriteString("\x1b[2J")
	os.Stdout.WriteString("\x1b[H")
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

func readKey() (key, error) {
	buf := make([]byte, 4)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if n > 0 {
			buf = bytes.TrimRightFunc(buf, func(r rune) bool { return r == 0 })
			switch {
			case bytes.Equal(buf, []byte("\x1b[A")):
				return keyArrowUp, nil
			case bytes.Equal(buf, []byte("\x1b[B")):
				return keyArrowDown, nil
			case bytes.Equal(buf, []byte("\x1b[C")):
				return keyArrowRight, nil
			case bytes.Equal(buf, []byte("\x1b[D")):
				return keyArrowLeft, nil
			case bytes.Equal(buf, []byte("\x1b[1~")), bytes.Equal(buf, []byte("\x1b[7~")),
				bytes.Equal(buf, []byte("\x1b[H")), bytes.Equal(buf, []byte("\x1bOH")):
				return keyHome, nil
			case bytes.Equal(buf, []byte("\x1b[4~")), bytes.Equal(buf, []byte("\x1b[8~")),
				bytes.Equal(buf, []byte("\x1b[F")), bytes.Equal(buf, []byte("\x1bOF")):
				return keyEnd, nil
			case bytes.Equal(buf, []byte("\x1b[3~")):
				return keyDelete, nil
			case bytes.Equal(buf, []byte("\x1b[5~")):
				return keyPageUp, nil
			case bytes.Equal(buf, []byte("\x1b[6~")):
				return keyPageDown, nil

			default:
				return key(buf[0]), nil
			}
		}
	}
}

func (e *Editor) MoveCursor(k key) {
	switch k {
	case keyArrowUp:
		if e.cy != 0 {
			e.cy--
		}
	case keyArrowDown:
		if e.cy < len(e.rows) {
			e.cy++
		}
	case keyArrowLeft:
		if e.cx != 0 {
			e.cx--
		} else if e.cy > 0 {
			e.cy--
			e.cx = len(e.rows[e.cy].chars)
		}
	case keyArrowRight:
		linelen := -1
		if e.cy < len(e.rows) {
			linelen = len(e.rows[e.cy].chars)
		}
		if linelen >= 0 && e.cx < linelen {
			e.cx++
		} else if linelen >= 0 && e.cx == linelen {
			e.cy++
			e.cx = 0
		}
	}

	var linelen int
	if e.cy < len(e.rows) {
		linelen = len(e.rows[e.cy].chars)
	}
	if e.cx > linelen {
		e.cx = linelen
	}
}

const quitTimes = 3

func (e *Editor) ProcessKey() error {
	k, err := readKey()
	if err != nil {
		return err
	}
	switch k {
	case keyEnter:
		e.InsertNewline()

	case key(ctrl('q')):
		if e.dirty > 0 && e.quitCounter < quitTimes {
			e.SetStatusMessage(
				"WARNING!!! File has unsaved changes. Press Ctrl-Q %d more times to quit.", quitTimes-e.quitCounter)
			e.quitCounter++
			return nil
		}
		os.Stdout.WriteString("\x1b[2J")
		os.Stdout.WriteString("\x1b[H")
		return ErrQuitEditor

	case key(ctrl('s')):
		n, err := e.Save()
		if err != nil {
			if err == ErrPromptCanceled {
				e.SetStatusMessage("Save aborted")
			} else {
				e.SetStatusMessage("Can't save! I/O error: %s", err.Error())
			}
		} else {
			e.SetStatusMessage("%d bytes written to disk", n)
		}

	case key(ctrl('f')):
		err := e.Find()
		if err != nil {
			if err == ErrPromptCanceled {
				e.SetStatusMessage("")
			} else {
				return err
			}
		}

	case keyHome:
		e.cx = 0

	case keyEnd:
		if e.cy < len(e.rows) {
			e.cx = len(e.rows[e.cy].chars)
		}

	case keyBackspace, key(ctrl('h')):
		e.DeleteChar()

	case keyDelete:
		if e.cy == len(e.rows)-1 && e.cx == len(e.rows[e.cy].chars) {
			break
		}
		e.MoveCursor(keyArrowRight)
		e.DeleteChar()

	case keyPageUp:
		e.cy = e.rowOffset

		for i := 0; i < e.screenRows; i++ {
			e.MoveCursor(keyArrowUp)
		}
	case keyPageDown:
		e.cy = e.rowOffset + e.screenRows - 1
		if e.cy > len(e.rows) {
			e.cy = len(e.rows)
		}
		for i := 0; i < e.screenRows; i++ {
			e.MoveCursor(keyArrowDown)
		}

	case keyArrowUp, keyArrowDown, keyArrowLeft, keyArrowRight:
		e.MoveCursor(k)

	case key(ctrl('l')), key('\x1b'):
		break

	default:
		e.InsertChar(rune(k))
	}

	e.quitCounter = 0
	return nil
}

func (e *Editor) drawRows(b *strings.Builder) {
	for y := 0; y < e.screenRows; y++ {
		filerow := y + e.rowOffset
		if filerow >= len(e.rows) {
			if len(e.rows) == 0 && y == e.screenRows/3 {
				welcomeMsg := fmt.Sprintf("Goeidt version %s", version)
				if runewidth.StringWidth(welcomeMsg) > e.screenCols {
					welcomeMsg = utf8Slice(welcomeMsg, 0, e.screenCols)
				}
				padding := (e.screenCols - runewidth.StringWidth(welcomeMsg)) / 2
				if padding > 0 {
					b.Write([]byte("~"))
					padding--
				}
				for ; padding > 0; padding-- {
					b.Write([]byte(" "))
				}
				b.WriteString(welcomeMsg)
			} else {
				b.Write([]byte("~"))
			}

		} else {
			var (
				line string
				hl   []uint8
			)
			if runewidth.StringWidth(e.rows[filerow].render) > e.colOffset {
				line = utf8Slice(
					e.rows[filerow].render,
					e.colOffset,
					utf8.RuneCountInString(e.rows[filerow].render))
				hl = e.rows[filerow].hl[e.colOffset:]
			}
			if runewidth.StringWidth(line) > e.screenCols {
				line = runewidth.Truncate(line, e.screenCols, "")
				hl = hl[:utf8.RuneCountInString(line)]
			}
			currentColor := -1
			for i, r := range []rune(line) {
				if unicode.IsControl(r) {
					sym := '?'
					if r < 26 {
						sym = '@' + r
					}
					b.WriteString("\x1b[7m")
					b.WriteRune(sym)
					b.WriteString("\x1b[m")
					if currentColor != -1 {

						b.WriteString(fmt.Sprintf("\x1b[%dm", currentColor))
					}
				} else if hl[i] == hlNormal {
					if currentColor != -1 {
						currentColor = -1
						b.WriteString("\x1b[39m")
					}
					b.WriteRune(r)
				} else {
					color := syntaxToColor(hl[i])
					if color != currentColor {
						currentColor = color
						b.WriteString(fmt.Sprintf("\x1b[%dm", color))
					}
					b.WriteRune(r)
				}
			}
			b.WriteString("\x1b[39m")
		}
		b.Write([]byte("\x1b[K"))
		b.Write([]byte("\r\n"))
	}
}

func (e *Editor) drawStatusBar(b *strings.Builder) {
	b.Write([]byte("\x1b[7m"))
	defer b.Write([]byte("\x1b[m"))
	filename := e.filename
	if utf8.RuneCountInString(filename) == 0 {
		filename = "[No Name]"
	}
	dirtyStatus := ""
	if e.dirty > 0 {
		dirtyStatus = "(modified)"
	}
	lmsg := fmt.Sprintf("%.20s - %d lines %s", filename, len(e.rows), dirtyStatus)
	if runewidth.StringWidth(lmsg) > e.screenCols {
		lmsg = runewidth.Truncate(lmsg, e.screenCols, "...")
	}
	b.WriteString(lmsg)
	filetype := "no filetype"
	if e.syntax != nil {
		filetype = e.syntax.filetype
	}
	rmsg := fmt.Sprintf("%s | %d/%d", filetype, e.cy+1, len(e.rows))
	l := runewidth.StringWidth(lmsg)
	for l < e.screenCols {
		if e.screenCols-l == runewidth.StringWidth(rmsg) {
			b.WriteString(rmsg)
			break
		}
		b.Write([]byte(" "))
		l++
	}
	b.Write([]byte("\r\n"))
}

func utf8Slice(s string, start, end int) string {
	return string([]rune(s)[start:end])
}

func (e *Editor) drawMessageBar(b *strings.Builder) {
	b.Write([]byte("\x1b[K"))
	msg := e.statusmsg
	if runewidth.StringWidth(msg) > e.screenCols {
		msg = runewidth.Truncate(msg, e.screenCols, "...")
	}

	if time.Since(e.statusmsgTime) < 5*time.Second {
		b.WriteString(msg)
	}
}

func rowCxToRx(row *Row, cx int) int {
	rx := 0
	for _, r := range row.chars[:cx] {
		if r == '\t' {
			rx += (tabstop) - (rx % tabstop)
		} else {
			rx += runewidth.RuneWidth(r)
		}
	}
	return rx
}

func rowRxToCx(row *Row, rx int) int {
	curRx := 0
	for i, r := range row.chars {
		if r == '\t' {
			curRx += (tabstop) - (curRx % tabstop)
		} else {
			curRx += runewidth.RuneWidth(r)
		}

		if curRx > rx {
			return i
		}
	}
	panic("unreachable")
}

func (e *Editor) scroll() {
	e.rx = 0
	if e.cy < len(e.rows) {
		e.rx = rowCxToRx(e.rows[e.cy], e.cx)
	}

	if e.cy < e.rowOffset {
		e.rowOffset = e.cy
	}

	if e.cy >= e.rowOffset+e.screenRows {
		e.rowOffset = e.cy - e.screenRows + 1
	}

	if e.rx < e.colOffset {
		e.colOffset = e.rx
	}

	if e.rx >= e.colOffset+e.screenCols {
		e.colOffset = e.rx - e.screenCols + 1
	}
}

func (e *Editor) Render() {
	e.scroll()

	var b strings.Builder

	b.Write([]byte("\x1b[?25l"))
	b.Write([]byte("\x1b[H"))

	e.drawRows(&b)
	e.drawStatusBar(&b)
	e.drawMessageBar(&b)

	b.WriteString(fmt.Sprintf("\x1b[%d;%dH", (e.cy-e.rowOffset)+1, (e.rx-e.colOffset)+1))

	b.Write([]byte("\x1b[?25h"))
	os.Stdout.WriteString(b.String())
}

func (e *Editor) SetStatusMessage(format string, a ...interface{}) {
	e.statusmsg = fmt.Sprintf(format, a...)
	e.statusmsgTime = time.Now()
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

func (e *Editor) rowsToString() string {
	var b strings.Builder
	for _, row := range e.rows {
		b.WriteString(string(row.chars))
		b.WriteRune('\n')
	}
	return b.String()
}

var ErrPromptCanceled = fmt.Errorf("user canceled the input prompt")

func (e *Editor) Prompt(prompt string, cb func(query string, k key)) (string, error) {
	var b strings.Builder
	for {
		e.SetStatusMessage(prompt, b.String())
		e.Render()

		k, err := readKey()
		if err != nil {
			return "", err
		}
		if k == keyDelete || k == keyBackspace || k == key(ctrl('h')) {
			if b.Len() > 0 {
				bytes := []byte(b.String())
				_, size := utf8.DecodeLastRune(bytes)
				b.Reset()
				b.WriteString(string(bytes[:len(bytes)-size]))
			}
		} else if k == key('\x1b') {
			e.SetStatusMessage("")
			if cb != nil {
				cb(b.String(), k)
			}
			return "", ErrPromptCanceled
		} else if k == keyEnter {
			if b.Len() > 0 {
				e.SetStatusMessage("")
				if cb != nil {
					cb(b.String(), k)
				}
				return b.String(), nil
			}
		} else if !unicode.IsControl(rune(k)) && !isArrowKey(k) && unicode.IsPrint(rune(k)) {
			b.WriteRune(rune(k))
		}

		if cb != nil {
			cb(b.String(), k)
		}
	}
}

func isArrowKey(k key) bool {
	return k == keyArrowUp || k == keyArrowRight ||
		k == keyArrowDown || k == keyArrowLeft
}

func (e *Editor) Save() (int, error) {

	if len(e.filename) == 0 {
		fname, err := e.Prompt("Save as: %s (ESC to cancel)", nil)
		if err != nil {
			return 0, err
		}
		e.filename = fname
		e.selectSyntaxHighlight()
	}

	f, err := os.OpenFile(e.filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := f.WriteString(e.rowsToString())
	if err != nil {
		return 0, err
	}
	e.dirty = 0
	return n, nil
}

func (e *Editor) OpenFile(filename string) error {
	e.filename = filename
	e.selectSyntaxHighlight()
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := s.Bytes()

		bytes.TrimRightFunc(line, func(r rune) bool { return r == '\n' || r == '\r' })
		e.InsertRow(len(e.rows), string(line))
	}
	if err := s.Err(); err != nil {
		return err
	}
	e.dirty = 0
	return nil
}

func (e *Editor) InsertRow(at int, chars string) {
	if at < 0 || at > len(e.rows) {
		return
	}
	row := &Row{chars: []rune(chars)}
	row.idx = at
	if at > 0 {
		row.hasUnclosedComment = e.rows[at-1].hasUnclosedComment
	}
	e.updateRow(row)

	e.rows = append(e.rows, &Row{})
	copy(e.rows[at+1:], e.rows[at:])
	for i := at + 1; i < len(e.rows); i++ {
		e.rows[i].idx++
	}
	e.rows[at] = row
}

func (e *Editor) InsertNewline() {
	if e.cx == 0 {
		e.InsertRow(e.cy, "")
	} else {
		row := e.rows[e.cy]
		e.InsertRow(e.cy+1, string(row.chars[e.cx:]))

		row = e.rows[e.cy]
		row.chars = row.chars[:e.cx]
		e.updateRow(row)
	}
	e.cy++
	e.cx = 0
}

func (e *Editor) updateRow(row *Row) {
	var b strings.Builder
	col := 0
	for _, r := range row.chars {
		if r == '\t' {

			b.WriteRune(' ')
			col++

			for col%tabstop != 0 {
				b.WriteRune(' ')
				col++
			}
		} else {
			b.WriteRune(r)
		}
	}
	row.render = b.String()
	e.updateHighlight(row)
}

func isSeparator(r rune) bool {
	return unicode.IsSpace(r) || strings.IndexRune(",.()+-/*=~%<>[]{}:;", r) != -1
}

func (e *Editor) updateHighlight(row *Row) {
	row.hl = make([]uint8, utf8.RuneCountInString(row.render))
	for i := range row.hl {
		row.hl[i] = hlNormal
	}

	if e.syntax == nil {
		return
	}

	prevSep := true

	var strQuote rune

	inComment := row.idx > 0 && e.rows[row.idx-1].hasUnclosedComment

	idx := 0
	runes := []rune(row.render)
	for idx < len(runes) {
		r := runes[idx]
		prevHl := hlNormal
		if idx > 0 {
			prevHl = row.hl[idx-1]
		}

		if e.syntax.scs != "" && strQuote == 0 && !inComment {
			if strings.HasPrefix(string(runes[idx:]), e.syntax.scs) {
				for idx < len(runes) {
					row.hl[idx] = hlComment
					idx++
				}
				break
			}
		}

		if e.syntax.mcs != "" && e.syntax.mce != "" && strQuote == 0 {
			if inComment {
				row.hl[idx] = hlMlComment
				if strings.HasPrefix(string(runes[idx:]), e.syntax.mce) {
					for j := 0; j < len(e.syntax.mce); j++ {
						row.hl[idx] = hlMlComment
						idx++
					}
					inComment = false
					prevSep = true
					continue
				} else {
					idx++
					continue
				}
			} else if strings.HasPrefix(string(runes[idx:]), e.syntax.mcs) {
				for j := 0; j < len(e.syntax.mcs); j++ {
					row.hl[idx] = hlMlComment
					idx++
				}
				inComment = true
				continue
			}
		}

		if (e.syntax.flags & HL_HIGHLIGHT_STRINGS) != 0 {
			if strQuote != 0 {
				row.hl[idx] = hlString

				if r == '\\' && idx+1 < len(runes) {
					row.hl[idx+1] = hlString
					idx += 2
					continue
				}
				if r == strQuote {
					strQuote = 0
				}
				idx++
				prevSep = true
				continue
			} else {
				if r == '"' || r == '\'' {
					strQuote = r
					row.hl[idx] = hlString
					idx++
					continue
				}
			}
		}

		if (e.syntax.flags & HL_HIGHLIGHT_NUMBERS) != 0 {
			if unicode.IsDigit(r) && (prevSep || prevHl == hlNumber) ||
				r == '.' && prevHl == hlNumber {
				row.hl[idx] = hlNumber
				idx++
				prevSep = false
				continue
			}
		}

		if prevSep {
			keywordFound := false
			for _, kw := range e.syntax.keywords {
				isKeyword2 := strings.HasSuffix(kw, "|")
				if isKeyword2 {
					kw = strings.TrimSuffix(kw, "|")
				}

				end := idx + utf8.RuneCountInString(kw)
				if end <= len(runes) && kw == string(runes[idx:end]) &&
					(end == len(runes) || isSeparator(runes[end])) {
					keywordFound = true
					hl := hlKeyword1
					if isKeyword2 {
						hl = hlKeyword2
					}
					for idx < end {
						row.hl[idx] = hl
						idx++
					}
					break
				}
			}
			if keywordFound {
				prevSep = false
				continue
			}
		}

		prevSep = isSeparator(r)
		idx++
	}

	changed := row.hasUnclosedComment != inComment
	row.hasUnclosedComment = inComment
	if changed && row.idx+1 < len(e.rows) {
		e.updateHighlight(e.rows[row.idx+1])
	}
}

func syntaxToColor(hl uint8) int {
	switch hl {
	case hlComment, hlMlComment:
		return 90
	case hlKeyword1:
		return 94
	case hlKeyword2:
		return 96
	case hlString:
		return 36
	case hlNumber:
		return 33
	case hlMatch:
		return 32
	default:
		return 37
	}
}

func (e *Editor) selectSyntaxHighlight() {
	e.syntax = nil
	if len(e.filename) == 0 {
		return
	}

	ext := filepath.Ext(e.filename)

	for _, syntax := range HLDB {
		for _, pattern := range syntax.filematch {
			isExt := strings.HasPrefix(pattern, ".")
			if (isExt && pattern == ext) ||
				(!isExt && strings.Index(e.filename, pattern) != -1) {
				e.syntax = syntax
				for _, row := range e.rows {
					e.updateHighlight(row)
				}
				return
			}
		}
	}
}

func (row *Row) insertChar(at int, c rune) {
	if at < 0 || at > len(row.chars) {
		at = len(row.chars)
	}
	row.chars = append(row.chars, 0)
	copy(row.chars[at+1:], row.chars[at:])
	row.chars[at] = c
}

func (row *Row) appendChars(chars []rune) {
	row.chars = append(row.chars, chars...)
}

func (row *Row) deleteChar(at int) {
	if at < 0 || at >= len(row.chars) {
		return
	}
	row.chars = append(row.chars[:at], row.chars[at+1:]...)
}

func (e *Editor) InsertChar(c rune) {
	if e.cy == len(e.rows) {
		e.InsertRow(len(e.rows), "")
	}
	row := e.rows[e.cy]
	row.insertChar(e.cx, c)
	e.updateRow(row)
	e.cx++
	e.dirty++
}

func (e *Editor) DeleteChar() {
	if e.cy == len(e.rows) {
		return
	}
	if e.cx == 0 && e.cy == 0 {
		return
	}
	row := e.rows[e.cy]
	if e.cx > 0 {
		row.deleteChar(e.cx - 1)
		e.updateRow(row)
		e.cx--
		e.dirty++
	} else {
		prevRow := e.rows[e.cy-1]
		e.cx = len(prevRow.chars)
		prevRow.appendChars(row.chars)
		e.updateRow(prevRow)
		e.DeleteRow(e.cy)
		e.cy--
	}
}

func (e *Editor) DeleteRow(at int) {
	if at < 0 || at >= len(e.rows) {
		return
	}
	e.rows = append(e.rows[:at], e.rows[at+1:]...)
	for i := at; i < len(e.rows); i++ {
		e.rows[i].idx--
	}
	e.dirty++
}

func (e *Editor) Find() error {
	savedCx := e.cx
	savedCy := e.cy
	savedColOffset := e.colOffset
	savedRowOffset := e.rowOffset

	lastMatchRowIndex := -1
	searchDirection := 1

	savedHlRowIndex := -1
	savedHl := []uint8(nil)

	onKeyPress := func(query string, k key) {
		if len(savedHl) > 0 {
			copy(e.rows[savedHlRowIndex].hl, savedHl)
			savedHl = []uint8(nil)
		}
		switch k {
		case keyEnter, key('\x1b'):
			lastMatchRowIndex = -1
			searchDirection = 1
			return
		case keyArrowRight, keyArrowDown:
			searchDirection = 1
		case keyArrowLeft, keyArrowUp:
			searchDirection = -1
		default:
			lastMatchRowIndex = -1
			searchDirection = 1
		}

		if lastMatchRowIndex == -1 {
			searchDirection = 1
		}

		current := lastMatchRowIndex

		for i := 0; i < len(e.rows); i++ {
			current += searchDirection
			switch current {
			case -1:
				current = len(e.rows) - 1
			case len(e.rows):
				current = 0
			}

			row := e.rows[current]
			rx := strings.Index(row.render, query)
			if rx != -1 {
				lastMatchRowIndex = current
				e.cy = current
				e.cx = rowRxToCx(row, rx)

				e.rowOffset = len(e.rows)
				savedHlRowIndex = current
				savedHl = make([]uint8, len(row.hl))
				copy(savedHl, row.hl)
				for i := 0; i < utf8.RuneCountInString(query); i++ {
					row.hl[rx+i] = hlMatch
				}
				break
			}
		}
	}

	_, err := e.Prompt("Search: %s (ESC = cancel | Enter = confirm | Arrows = prev/next)", onKeyPress)

	if err == ErrPromptCanceled {
		e.cx = savedCx
		e.cy = savedCy
		e.colOffset = savedColOffset
		e.rowOffset = savedRowOffset
	}
	return err
}

func main() {
	var editor Editor

	if err := editor.Init(); err != nil {
		die(err)
	}
	defer editor.Close()

	if len(os.Args) > 1 {
		err := editor.OpenFile(os.Args[1])
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			die(err)
		}
	}

	editor.SetStatusMessage("HELP: Ctrl-S - save | Ctrl-Q - quit | Ctrl-F - find")

	for {
		editor.Render()
		if err := editor.ProcessKey(); err != nil {
			if err == ErrQuitEditor {
				break
			}
			die(err)
		}
	}
}
