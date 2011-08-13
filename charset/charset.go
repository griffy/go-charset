package charset

import (
	"fmt"
	"io"
	"io/ioutil"
	"json"
	"os"
	"strings"
	"sync"
	"utf8"
)

const errNotFound = os.NewError("charset: character set not found")

// CharsetDir is the data file directory.
var CharsetDir = "/usr/local/lib/go-charset/data"

func init() {
	root := os.Getenv("GOROOT")
	if root != "" {
		CharsetDir = root + "/src/pkg/go-charset.googlecode.com/hg/data"
	}
}

// A general cache store that character set translators
// can use for persistent storage of data.
var (
	cacheMutex sync.Mutex
	cacheStore = make(map[interface{}]interface{})
)

// charsetEntry is the data structure for one entry in the JSON config file.
// If Alias is non-empty, it should be the canonical name of another
// character set; otherwise Class should be the name
// of an entry in classes, and Arg is the argument for
// instantiating it.
type charsetEntry struct {
	Aliases []string
	Desc    string
	Class   string
	Arg     string
}

// Charset holds information about a given character set.
type Charset struct {
	Name           string                        // Canonical name of character set.
	Aliases        []string                      // Known aliases.
	Desc           string                        // Description.
	TranslatorFrom func() (Translator, os.Error) // Create a Translator from this character set.
	TranslatorTo   func() (Translator, os.Error) // Create a Translator To this character set.
}

// Translator represents a character set converter.
// The Translate method translates the given data,
// and returns the number of bytes of data consumed,
// a slice containing the converted data (which may be
// overwritten on the next call to Translate), and any
// conversion error. If eof is true, the data represents
// the final bytes of the input.
type Translator interface {
	Translate(data []byte, eof bool) (n int, cdata []byte, err os.Error)
}

var (
	readCharsetsOnce sync.Once
	charsets         = make(map[string]*Charset)
)

// A class of character sets.
// Each class of can be instantiated with an argument specified in the config file.
// Many character sets can use a single class.
type class struct {
	from, to func(arg string) (Translator, os.Error)
}

// The set of classes, indexed by class name.
var classes = make(map[string]*class)

func registerClass(charset string, from, to func(arg string) (Translator, os.Error)) {
	classes[charset] = &class{from, to}
}

// Register registers a new character set. If override is true,
// any existing character sets and aliases will be overridden.
// All names and aliases in cs are normalised with NormalizedName.
func (cs *Charset) Register(override bool) {
	cs.Name = NormalizedName(cs.Name)
	if !override && charsets[cs.Name] != nil {
		return
	}
	charsets[cs.Name] = cs
	for i, alias := range cs.Aliases {
		alias = NormalizedName(alias)
		cs.Aliases[i] = alias
		if charsets[alias] == nil || override {
			charsets[alias] = cs
		}
	}
}

// readCharsets reads the JSON config file.
// It's done once only, when first needed.
func readCharsets() {
	file := filename("charsets.json")
	csdata, err := os.Open(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "charset: cannot open %q: %v\n", file, err)
		return
	}

	var entries map[string]charsetEntry
	dec := json.NewDecoder(csdata)
	err = dec.Decode(&entries)
	if err != nil {
		fmt.Fprintf(os.Stderr, "charset: cannot decode %q: %v\n", file, err)
	}
	for name, e := range entries {
		name = NormalizedName(name)
		class := classes[e.Class]
		if class == nil {
			continue
		}
		cs := &Charset{
			Name:    name,
			Aliases: e.Aliases,
			Desc:    e.Desc,
		}
		arg := e.Arg
		if class.from != nil {
			cs.TranslatorFrom = func() (Translator, os.Error) {
				return class.from(arg)
			}
		}
		if class.to != nil {
			cs.TranslatorTo = func() (Translator, os.Error) {
				return class.to(arg)
			}
		}
		cs.Register(false)
	}
}

// NewReader returns a new Reader that translates from the named
// character set to UTF-8 as it reads r.
func NewReader(charset string, r io.Reader) (io.Reader, os.Error) {
	cs := Info(charset)
	if cs == nil {
		return nil, errNotFound
	}
	tr, err := cs.TranslatorFrom()
	if err != nil {
		return nil, err
	}
	return NewTranslatingReader(r, tr), nil
}

// NewWriter returns a new WriteCloser writing to w.  It converts writes
// of UTF-8 text into writes on w of text in the named character set.
// The Close is necessary to flush any remaining partially translated
// characters to the output.
func NewWriter(charset string, w io.Writer) (io.WriteCloser, os.Error) {
	cs := Info(charset)
	if cs == nil {
		return nil, errNotFound
	}
	tr, err := cs.TranslatorTo()
	if err != nil {
		return nil, err
	}
	return NewTranslatingWriter(w, tr), nil
}

// Info returns information about a character set, or nil
// if the character set is not found.
func Info(name string) *Charset {
	readCharsetsOnce.Do(readCharsets)
	return charsets[NormalizedName(name)]
}

// Names returns the canonical names of all supported character sets.
func Names() []string {
	var names []string
	readCharsetsOnce.Do(readCharsets)
	for name, cs := range charsets {
		if charsets[cs.Name] == cs {
			names = append(names, name)
		}
	}
	return names
}

func normalizedChar(c int) int {
	switch {
	case c >= 'A' && c <= 'Z':
		c = c - 'A' + 'a'
	case c == '_':
		c = '-'
	}
	return c
}

// NormalisedName returns s with all Roman capitals
// mapped to lower case, and '_' mapped to '-'
func NormalizedName(s string) string {
	return strings.Map(normalizedChar, s)
}

// filename returns the location of a file named f inside the data directory.
func filename(f string) string {
	if f != "" && f[0] == '/' {
		return f
	}
	return CharsetDir + "/" + f
}

type translatingWriter struct {
	w   io.Writer
	tr  Translator
	buf []byte // unconsumed data from writer.
}

// NewTranslatingWriter returns a new WriteCloser writing to w.
// It passes the written bytes through the given Translator.
func NewTranslatingWriter(w io.Writer, tr Translator) io.WriteCloser {
	return &translatingWriter{w: w, tr: tr}
}

func (w *translatingWriter) Write(data []byte) (rn int, rerr os.Error) {
	wdata := data
	if len(w.buf) > 0 {
		w.buf = append(w.buf, data...)
		wdata = w.buf
	}
	n, cdata, err := w.tr.Translate(wdata, false)
	if err != nil {
		// TODO
	}
	if n > 0 {
		_, err = w.w.Write(cdata)
		if err != nil {
			return 0, err
		}
	}
	w.buf = w.buf[:0]
	if n < len(wdata) {
		w.buf = append(w.buf, wdata[n:]...)
	}
	return len(data), nil
}

func (p *translatingWriter) Close() os.Error {
	for {
		n, data, err := p.tr.Translate(p.buf, true)
		p.buf = p.buf[n:]
		if err != nil {
			// TODO
		}
		// If the Translator produces no data
		// at EOF, then assume that it never will.
		if len(data) == 0 {
			break
		}
		n, err = p.w.Write(data)
		if err != nil {
			return err
		}
		if n < len(data) {
			return io.ErrShortWrite
		}
		if len(p.buf) == 0 {
			break
		}
	}
	return nil
}

type translatingReader struct {
	r     io.Reader
	tr    Translator
	cdata []byte   // unconsumed data from converter.
	rdata []byte   // unconverted data from reader.
	err   os.Error // final error from reader.
}

// NewTranslatingReader returns a new Reader that
// translates data using the given Translator as it reads r.   
func NewTranslatingReader(r io.Reader, tr Translator) io.Reader {
	return &translatingReader{r: r, tr: tr}
}

func (r *translatingReader) Read(buf []byte) (int, os.Error) {
	for {
		if len(r.cdata) > 0 {
			n := copy(buf, r.cdata)
			r.cdata = r.cdata[n:]
			return n, nil
		}
		if r.err == nil {
			r.rdata = ensureCap(r.rdata, len(r.rdata)+len(buf))
			n, err := r.r.Read(r.rdata[len(r.rdata):cap(r.rdata)])
			// Guard against non-compliant Readers.
			if n == 0 && err == nil {
				err = os.EOF
			}
			r.rdata = r.rdata[0 : len(r.rdata)+n]
			r.err = err
		} else if len(r.rdata) == 0 {
			break
		}
		nc, cdata, cvterr := r.tr.Translate(r.rdata, r.err != nil)
		if cvterr != nil {
			// TODO
		}
		r.cdata = cdata

		// Ensure that we consume all bytes at eof
		// if the converter refuses them.
		if nc == 0 && r.err != nil {
			nc = len(r.rdata)
		}

		// Copy unconsumed data to the start of the rdata buffer.
		r.rdata = r.rdata[0:copy(r.rdata, r.rdata[nc:])]
	}
	return 0, r.err
}

// ensureCap returns s with a capacity of at least n bytes.
// If cap(s) < n, then it returns a new copy of s with the
// required capacity.
func ensureCap(s []byte, n int) []byte {
	if n <= cap(s) {
		return s
	}
	// logic adapted from appendslice1 in runtime
	m := cap(s)
	if m == 0 {
		m = n
	} else {
		for {
			if m < 1024 {
				m += m
			} else {
				m += m / 4
			}
			if m >= n {
				break
			}
		}
	}
	t := make([]byte, len(s), m)
	copy(t, s)
	return t
}

func appendRune(buf []byte, r int) []byte {
	n := len(buf)
	buf = ensureCap(buf, n+utf8.UTFMax)
	nu := utf8.EncodeRune(buf[n:n+utf8.UTFMax], r)
	return buf[0 : n+nu]
}

func readFile(name string) ([]byte, os.Error) {
	file := filename(name)
	fd, err := os.Open(file)
	if fd == nil {
		return nil, err
	}
	data, err := ioutil.ReadAll(fd)
	if err != nil {
		return nil, fmt.Errorf("error reading %q: %v", file, err)
	}
	return data, nil
}

func cache(key interface{}, f func() (interface{}, os.Error)) (interface{}, os.Error) {
	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	if x := cacheStore[key]; x != nil {
		return x, nil
	}
	x, err := f()
	if err != nil {
		return nil, err
	}
	cacheStore[key] = x
	return x, err
}
