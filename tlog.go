// tlog is an logger and a tracer in the one package.
//
package tlog

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nikandfor/json"
)

type (
	ID int64

	Labels []string

	Logger struct {
		Writer
		level int
	}

	Writer interface {
		Labels(ls Labels)
		SpanStarted(s *Span, l Location)
		SpanFinished(s *Span, el time.Duration)
		Message(l Message, s *Span)
	}

	Message struct {
		Location Location
		Time     time.Duration
		Format   string
		Args     []interface{}
	}

	Span struct {
		l *Logger

		ID     ID
		Parent ID

		Started time.Time

		Flags int
	}

	ConsoleWriter struct {
		mu        sync.Mutex
		w         io.Writer
		f         int
		shortfile int
		funcname  int
		buf       bufWriter
	}

	JSONWriter struct {
		mu  sync.Mutex
		w   *json.Writer
		ls  map[Location]struct{}
		buf []byte
	}

	TeeWriter struct {
		mu      sync.Mutex
		Writers []Writer
	}

	Rand interface {
		Int63() int64
	}

	concurrentRand struct {
		mu  sync.Mutex
		rnd Rand
	}

	bufWriter []byte
)

const ( // log levels
	LevCritical = iota
	LevError
	LevInfo
	LevDebug
	LevTrace
)

const ( // flags
	FlagError = 1 << iota

	FlagNone = 0
)

const ( // console writer flags
	Ldate = 1 << iota
	Ltime
	Lmilliseconds
	Lmicroseconds
	Lshortfile
	Llongfile
	Ltypefunc // pkg.(*Type).Func
	Lfuncname // Func
	LUTC
	Lspans       // print Span start and finish event
	Lmessagespan // add Span ID to trace messages
	LstdFlags    = Ldate | Ltime
	LdetFlags    = Ldate | Ltime | Lmicroseconds | Lshortfile
)

var ( // time, rand
	now      = time.Now
	rnd Rand = &concurrentRand{rnd: rand.New(rand.NewSource(now().UnixNano()))}

	digits = []byte("0123456789abcdef")
)

var ( // defaults
	DefaultLogger = New(NewConsoleWriter(os.Stderr, LstdFlags))
)

func FillLabelsWithDefaults(labels ...string) Labels {
	var ll Labels

	for _, lab := range labels {
		switch {
		case strings.HasPrefix(lab, "_hostname"):
			if lab != "_hostname" {
				break
			}
			h, err := os.Hostname()
			if h == "" && err != nil {
				h = err.Error()
			}

			ll.Set("_hostname", h)

			continue
		case strings.HasPrefix(lab, "_pid"):
			if lab != "_pid" {
				break
			}

			ll.Set("_pid", fmt.Sprintf("%d", os.Getpid()))

			continue
		}

		ll = append(ll, lab)
	}

	return ll
}

func New(w Writer) *Logger {
	l := &Logger{Writer: w, level: LevInfo}

	return l
}

func Printf(f string, args ...interface{}) {
	newmessage(DefaultLogger, nil, f, args)
}

func Panicf(f string, args ...interface{}) {
	newmessage(DefaultLogger, nil, f, args)
	panic(fmt.Sprintf(f, args...))
}

func Fatalf(f string, args ...interface{}) {
	newmessage(DefaultLogger, nil, f, args)
	os.Exit(1)
}

func V(l int) *Logger {
	return DefaultLogger.V(l)
}

func newspan(l *Logger, par ID) *Span {
	loc := funcentry(2)
	s := &Span{
		l:       l,
		ID:      ID(rnd.Int63()),
		Parent:  par,
		Started: now(),
	}
	l.SpanStarted(s, loc)
	return s
}

func newmessage(l *Logger, s *Span, f string, args []interface{}) {
	if l == nil {
		return
	}

	var t time.Duration
	if s == nil {
		t = time.Duration(now().UnixNano())
	} else {
		t = now().Sub(s.Started)
	}

	l.Message(
		Message{
			Location: location(2),
			Time:     t,
			Format:   f,
			Args:     args,
		},
		s,
	)
}

func Start() *Span {
	if DefaultLogger == nil {
		return nil
	}

	return newspan(DefaultLogger, 0)
}

func Spawn(id ID) *Span {
	if DefaultLogger == nil || id == 0 {
		return nil
	}

	return newspan(DefaultLogger, id)
}

func (l *Logger) Printf(f string, args ...interface{}) {
	newmessage(l, nil, f, args)
}

func (l *Logger) Panicf(f string, args ...interface{}) {
	newmessage(l, nil, f, args)
	panic(fmt.Sprintf(f, args...))
}

func (l *Logger) Fatalf(f string, args ...interface{}) {
	newmessage(l, nil, f, args)
	os.Exit(1)
}

func (l *Logger) Start() *Span {
	if l == nil {
		return nil
	}

	return newspan(l, 0)
}

func (l *Logger) Spawn(id ID) *Span {
	if l == nil || id == 0 {
		return nil
	}

	return newspan(l, id)
}

func (l *Logger) V(lv int) *Logger {
	if l == nil || lv > l.level {
		return nil
	}
	return l
}

func (l *Logger) SetLogLevel(v int) {
	if l == nil {
		return
	}
	l.level = v
}

func (s *Span) Printf(f string, args ...interface{}) {
	if s == nil {
		return
	}

	newmessage(s.l, s, f, args)
}

func (s *Span) Finish() {
	if s == nil {
		return
	}

	el := now().Sub(s.Started)
	s.l.SpanFinished(s, el)
}

func (s *Span) SafeID() ID {
	if s == nil {
		return 0
	}
	return s.ID
}

func NewConsoleWriter(w io.Writer, f int) *ConsoleWriter {
	return &ConsoleWriter{
		w:         w,
		f:         f,
		shortfile: 20,
		funcname:  17,
	}
}

func (w *ConsoleWriter) grow(b []byte, l int) []byte {
more:
	b = b[:cap(b)]
	if len(b) >= l {
		return b
	}

	b = append(b,
		0, 0, 0, 0, 0,
		0, 0, 0, 0, 0,
		0, 0, 0, 0, 0,
		0, 0, 0, 0, 0)

	goto more
}

func (w *ConsoleWriter) appendSegments(b []byte, i, wid int, name string, s byte) ([]byte, int) {
	b = w.grow(b, i+wid+5)
	W := i + wid

	if nl := len(name); nl <= wid {
		i += copy(b[i:], name)
		for j := i; j < W; j++ {
			b[j] = ' '
		}
		return b, i
	}

	for i+2 < W {
		if len(name) <= W-i {
			i += copy(b[i:], name)
			break
		}

		p := strings.IndexByte(name, s)
		if p == -1 {
			i += copy(b[i:], name[:W-i])
			break
		}

		if len(name)-p < W-i {
			copy(b[i:], name)
			i = W - (len(name) - p)

			b[i] = s
			i++
		} else {
			b[i] = name[0]
			i++
			b[i] = s
			i++
		}

		name = name[p+1:]
	}

	return b, i
}

func (w *ConsoleWriter) buildHeader(t time.Time, loc Location) {
	b := w.buf
	b = b[:cap(b)]
	i := 0

	var fname, file string
	var line int = -1

	if w.f&(Ldate|Ltime|Lmilliseconds|Lmicroseconds) != 0 {
		if w.f&LUTC != 0 {
			t = t.UTC()
		}
		if w.f&Ldate != 0 {
			b = w.grow(b, i+15)

			y, m, d := t.Date()
			for j := 3; j >= 0; j-- {
				b[i+j] = '0' + byte(y%10)
				y /= 10
			}
			i += 4

			b[i] = '/'
			i++

			b[i] = '0' + byte(m/10)
			i++
			b[i] = '0' + byte(m%10)
			i++

			b[i] = '/'
			i++

			b[i] = '0' + byte(d/10)
			i++
			b[i] = '0' + byte(d%10)
			i++
		}
		if w.f&Ltime != 0 {
			b = w.grow(b, i+12)

			if i != 0 {
				b[i] = '_'
				i++
			}

			h, m, s := t.Clock()

			b[i] = '0' + byte(h/10)
			i++
			b[i] = '0' + byte(h%10)
			i++

			b[i] = ':'
			i++

			b[i] = '0' + byte(m/10)
			i++
			b[i] = '0' + byte(m%10)
			i++

			b[i] = ':'
			i++

			b[i] = '0' + byte(s/10)
			i++
			b[i] = '0' + byte(s%10)
			i++
		}
		if w.f&(Lmilliseconds|Lmicroseconds) != 0 {
			b = w.grow(b, i+12)

			if i != 0 {
				b[i] = '.'
				i++
			}

			ns := t.Nanosecond() / 1e3
			n := 6
			if w.f&Lmilliseconds != 0 {
				n = 3
				ns /= 1e3
			}
			for j := n - 1; j >= 0; j-- {
				b[i+j] = '0' + byte(ns%10)
				ns /= 10
			}
			i += n
		}
		b[i] = ' '
		i++
		b[i] = ' '
		i++
	}
	if w.f&(Llongfile|Lshortfile) != 0 {
		fname, file, line = loc.NameFileLine()

		if w.f&Lshortfile != 0 {
			file = path.Base(file)
		}

		j := 0
		for q := 10; q < line; q *= 10 {
			j++
		}
		n := 1 + j

		var st int
		if w.f&Lshortfile != 0 {
			b, st = w.appendSegments(b, i, w.shortfile-n-1, file, '/')
		} else {
			b = append(b[:i], file...)
			i += len(file)
			st = i
		}

		b = w.grow(b, st+10)

		b[st] = ':'
		st++

		for ; j >= 0; j-- {
			b[st+j] = '0' + byte(line%10)
			line /= 10
		}
		st += n

		if w.f&Lshortfile != 0 {
			W := i + w.shortfile
			for ; st < W; st++ {
				b[st] = ' '
			}
			i += w.shortfile
		} else {
			i = st
		}

		b[i] = ' '
		i++
		b[i] = ' '
		i++
	}
	if w.f&(Ltypefunc|Lfuncname) != 0 {
		if line == -1 {
			fname, file, line = loc.NameFileLine()
		}
		fname = path.Base(fname)

		if w.f&Lfuncname != 0 {
			p := strings.Index(fname, ").")
			if p == -1 {
				p = strings.IndexByte(fname, '.')
				fname = fname[p+1:]
			} else {
				fname = fname[p+2:]
			}

			if l := len(fname); l <= w.funcname {
				W := i + w.funcname
				b = w.grow(b, W+4)
				i += copy(b[i:], fname)
				for ; i < W; i++ {
					b[i] = ' '
				}
			} else {
				i += copy(b[i:], fname[:w.funcname])
				j := 1
				for {
					q := fname[l-j]
					if q < '0' || '9' < q {
						break
					}
					b[i-j] = fname[l-j]
					j++
				}
			}
		} else {
			b = append(b[:i], fname...)
			i += len(fname)
		}

		b = w.grow(b, i+4)

		b[i] = ' '
		i++
		b[i] = ' '
		i++
	}

	w.buf = b[:i]
}

func (w *ConsoleWriter) Message(m Message, s *Span) {
	defer w.mu.Unlock()
	w.mu.Lock()

	var t time.Time
	if s != nil {
		t = s.Started.Add(m.Time)
	} else {
		t = m.AbsTime()
	}

	w.buildHeader(t, m.Location)

	if s != nil && w.f&Lmessagespan != 0 {
		b := append(w.buf, "Span "...)
		i := len(b)
		b = w.grow(b, i+20)

		id := s.ID
		for j := 15; j >= 0; j-- {
			b[i+j] = digits[id&0xf]
			id >>= 4
		}
		i += 16

		b[i] = ' '
		i++

		w.buf = b[:i]
	}

	_, _ = fmt.Fprintf(&w.buf, m.Format, m.Args...)

	w.buf.NewLine()

	_, _ = w.w.Write(w.buf)
}

func (w *ConsoleWriter) spanHeader(s *Span, tm time.Time, loc Location) []byte {
	w.buildHeader(tm, loc)

	b := w.buf

	b = append(b, "Span "...)
	i := len(b)
	b = b[:i]

	b = w.grow(b, i+40)

	id := s.ID
	for j := 15; j >= 0; j-- {
		b[i+j] = digits[id&0xf]
		id >>= 4
	}
	i += 16

	i += copy(b[i:], " par ")

	id = s.Parent
	if id == 0 {
		for j := 15; j >= 0; j-- {
			b[i+j] = '_'
		}
	} else {
		for j := 15; j >= 0; j-- {
			b[i+j] = digits[id&0xf]
			id >>= 4
		}
	}
	i += 16

	b[i] = ' '
	i++
	//	b[i] = ' '
	//	i++

	b = b[:i]

	//	b = append(b, loc...)

	return b
}

func (w *ConsoleWriter) SpanStarted(s *Span, l Location) {
	if w.f&Lspans == 0 {
		return
	}

	defer w.mu.Unlock()
	w.mu.Lock()

	b := w.spanHeader(s, s.Started, l)

	b = append(b, "started\n"...)

	w.buf = b

	_, _ = w.w.Write(b)
}

func (w *ConsoleWriter) SpanFinished(s *Span, el time.Duration) {
	if w.f&Lspans == 0 {
		return
	}

	defer w.mu.Unlock()
	w.mu.Lock()

	b := w.spanHeader(s, s.Started.Add(el), 0)

	b = append(b, "finished - elapsed "...)
	i := len(b)

	e := el.Seconds() * 1000

	b = strconv.AppendFloat(b, e, 'f', 2, 64)
	b = append(b, "ms"...)

	if s.Flags != 0 {
		b = append(b, " Flags "...)
		i = len(b)
		b = w.grow(b, i+18)

		F := s.Flags
		j := 0
		for q := uint64(0xf); q <= uint64(F) && j < 15; q <<= 4 {
			j++
		}
		n := j + 1
		for ; j >= 0; j-- {
			b[i+j] = digits[F&0xf]
			F >>= 4
		}
		i += n

		b = b[:i]
	}

	b = append(b, '\n')

	w.buf = b

	_, _ = w.w.Write(b)
}

func (w *ConsoleWriter) Labels(ls Labels) {
	w.Message(
		Message{
			Location: location(1),
			Time:     time.Duration(now().UnixNano()),
			Format:   "Labels: %q",
			Args:     []interface{}{ls},
		},
		nil,
	)
}

func NewJSONWriter(w io.Writer) *JSONWriter {
	return NewCustomJSONWriter(json.NewStreamWriter(w))
}

func NewCustomJSONWriter(w *json.Writer) *JSONWriter {
	return &JSONWriter{
		w:  w,
		ls: make(map[Location]struct{}),
	}
}

func (w *JSONWriter) Labels(ls Labels) {
	defer w.w.Flush()
	defer w.mu.Unlock()
	w.mu.Lock()

	w.w.ObjStart()

	w.w.ObjKey([]byte("L"))

	w.w.ArrayStart()

	for _, l := range ls {
		w.w.SafeStringString(l)
	}

	w.w.ArrayEnd()

	w.w.ObjEnd()

	w.w.NewLine()
}

func (w *JSONWriter) Message(m Message, s *Span) {
	defer w.w.Flush()
	defer w.mu.Unlock()
	w.mu.Lock()

	if _, ok := w.ls[m.Location]; !ok {
		w.location(m.Location)
	}

	b := w.buf

	w.w.ObjStart()

	w.w.ObjKey([]byte("m"))

	w.w.ObjStart()

	w.w.ObjKey([]byte("l"))
	b = strconv.AppendInt(b[:0], int64(m.Location), 10)
	_, _ = w.w.Write(b)

	w.w.ObjKey([]byte("t"))
	b = strconv.AppendInt(b[:0], m.Time.Nanoseconds()/1000, 10)
	_, _ = w.w.Write(b)

	w.w.ObjKey([]byte("m"))
	sw := w.w.StringWriter()
	_, _ = fmt.Fprintf(sw, m.Format, m.Args...)
	sw.Close()

	if s != nil {
		w.w.ObjKey([]byte("s"))
		b = strconv.AppendInt(b[:0], int64(s.ID), 10)
		_, _ = w.w.Write(b)
	}

	w.w.ObjEnd()

	w.w.ObjEnd()

	w.w.NewLine()

	w.buf = b
}

func (w *JSONWriter) SpanStarted(s *Span, loc Location) {
	defer w.w.Flush()
	defer w.mu.Unlock()
	w.mu.Lock()

	if _, ok := w.ls[loc]; !ok {
		w.location(loc)
	}

	b := w.buf

	w.w.ObjStart()

	w.w.ObjKey([]byte("s"))

	w.w.ObjStart()

	w.w.ObjKey([]byte("id"))
	b = strconv.AppendInt(b[:0], int64(s.ID), 10)
	_, _ = w.w.Write(b)

	if s.Parent != 0 {
		w.w.ObjKey([]byte("p"))
		b = strconv.AppendInt(b[:0], int64(s.Parent), 10)
		_, _ = w.w.Write(b)
	}

	w.w.ObjKey([]byte("l"))
	b = strconv.AppendInt(b[:0], int64(loc), 10)
	_, _ = w.w.Write(b)

	w.w.ObjKey([]byte("s"))
	b = strconv.AppendInt(b[:0], s.Started.UnixNano()/1000, 10)
	_, _ = w.w.Write(b)

	w.w.ObjEnd()

	w.w.ObjEnd()

	w.w.NewLine()

	w.buf = b
}

func (w *JSONWriter) SpanFinished(s *Span, el time.Duration) {
	defer w.w.Flush()
	defer w.mu.Unlock()
	w.mu.Lock()

	b := w.buf

	w.w.ObjStart()

	w.w.ObjKey([]byte("f"))

	w.w.ObjStart()

	w.w.ObjKey([]byte("id"))
	b = strconv.AppendInt(b[:0], int64(s.ID), 10)
	_, _ = w.w.Write(b)

	w.w.ObjKey([]byte("e"))
	b = strconv.AppendInt(b[:0], el.Nanoseconds()/1000, 10)
	_, _ = w.w.Write(b)

	if s.Flags != 0 {
		w.w.ObjKey([]byte("F"))
		b = strconv.AppendInt(b[:0], int64(s.Flags), 10)
		_, _ = w.w.Write(b)
	}

	w.w.ObjEnd()

	w.w.ObjEnd()

	w.w.NewLine()

	w.buf = b
}

func (w *JSONWriter) location(l Location) {
	name, file, line := l.NameFileLine()
	name = path.Base(name)

	b := w.buf

	w.w.ObjStart()

	w.w.ObjKey([]byte("l"))

	w.w.ObjStart()

	w.w.ObjKey([]byte("pc"))
	b = strconv.AppendInt(b[:0], int64(l), 10)
	_, _ = w.w.Write(b)

	w.w.ObjKey([]byte("f"))
	w.w.SafeStringString(file)

	w.w.ObjKey([]byte("l"))
	b = strconv.AppendInt(b[:0], int64(line), 10)
	_, _ = w.w.Write(b)

	w.w.ObjKey([]byte("n"))
	w.w.StringString(name)

	w.w.ObjEnd()

	w.w.ObjEnd()

	w.w.NewLine()

	w.ls[l] = struct{}{}
	w.buf = b
}

func NewTeeWriter(w ...Writer) *TeeWriter {
	return &TeeWriter{Writers: w}
}

func (w *TeeWriter) Labels(ls Labels) {
	defer w.mu.Unlock()
	w.mu.Lock()

	for _, w := range w.Writers {
		w.Labels(ls)
	}
}

func (w *TeeWriter) Message(m Message, s *Span) {
	defer w.mu.Unlock()
	w.mu.Lock()

	for _, w := range w.Writers {
		w.Message(m, s)
	}
}

func (w *TeeWriter) SpanStarted(s *Span, loc Location) {
	defer w.mu.Unlock()
	w.mu.Lock()

	for _, w := range w.Writers {
		w.SpanStarted(s, loc)
	}
}

func (w *TeeWriter) SpanFinished(s *Span, el time.Duration) {
	defer w.mu.Unlock()
	w.mu.Lock()

	for _, w := range w.Writers {
		w.SpanFinished(s, el)
	}
}

func (ls *Labels) Set(k, v string) {
	val := k
	if v != "" {
		val += "=" + v
	}

	for i := 0; i < len(*ls); i++ {
		l := (*ls)[i]
		if l == "="+k {
			(*ls)[i] = val
			return
		} else if l == k || strings.HasPrefix(l, k+"=") {
			(*ls)[i] = val
			return
		}
	}
	*ls = append(*ls, val)
}

func (ls *Labels) Get(k string) (string, bool) {
	for _, l := range *ls {
		if l == k {
			return "", true
		} else if strings.HasPrefix(l, k+"=") {
			return l[len(k)+1:], true
		}
	}
	return "", false
}

func (ls *Labels) Del(k string) {
	for i := 0; i < len(*ls); i++ {
		l := (*ls)[i]
		if l == "="+k {
			return
		} else if l == k || strings.HasPrefix(l, k+"=") {
			(*ls)[i] = "=" + k
		}
	}
}

func (ls *Labels) Merge(b Labels) {
	for _, add := range b {
		if add == "" {
			continue
		}
		kv := strings.SplitN(add, "=", 2)
		if kv[0] == "" {
			ls.Del(kv[1])
		} else {
			ls.Set(kv[0], kv[1])
		}
	}
}

func (i ID) String() string {
	if i == 0 {
		return "________________"
	}
	return fmt.Sprintf("%016x", uint64(i))
}

func (m *Message) AbsTime() time.Time {
	return time.Unix(0, int64(m.Time))
}

func (m *Message) SpanID() ID {
	if m == nil || m.Args == nil {
		return 0
	}
	return m.Args[0].(ID)
}

func (r *concurrentRand) Int63() int64 {
	defer r.mu.Unlock()
	r.mu.Lock()

	return r.rnd.Int63()
}

func (w *bufWriter) Write(p []byte) (int, error) {
	*w = append(*w, p...)
	return len(p), nil
}

func (w *bufWriter) NewLine() {
	l := len(*w)
	if l == 0 || (*w)[l-1] != '\n' {
		*w = append(*w, '\n')
	}
}
