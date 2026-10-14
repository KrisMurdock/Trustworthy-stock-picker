package logging

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Dir              string
	MaxBytes         int64
	MaxDays, Backups int
	JSON             bool
	Color            string
	Stdout           io.Writer
}
type Logger struct {
	mu      sync.Mutex
	opts    Options
	file    *os.File
	size    int64
	day     string
	seq     int
	secrets []string
}

func Nop() *Logger { return &Logger{} }

func EnvOptions() (Options, error) {
	o := Options{Dir: "logs", MaxBytes: 10 << 20, MaxDays: 7, Backups: 10, Color: "auto", Stdout: os.Stdout}
	if v := os.Getenv("STOCK_WATCHER_LOG_DIR"); v != "" {
		o.Dir = v
	}
	for _, e := range []struct {
		name string
		dst  *int
		def  int
	}{{"STOCK_WATCHER_LOG_MAX_MB", nil, 10}, {"STOCK_WATCHER_LOG_MAX_DAYS", &o.MaxDays, 7}, {"STOCK_WATCHER_LOG_BACKUPS", &o.Backups, 10}} {
		v := e.def
		if raw, ok := os.LookupEnv(e.name); ok {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 1000 {
				return o, fmt.Errorf("invalid %s", e.name)
			}
			v = n
		}
		if e.dst == nil {
			o.MaxBytes = int64(v) << 20
		} else {
			*e.dst = v
		}
	}
	if v, ok := os.LookupEnv("STOCK_WATCHER_LOG_JSON"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return o, errors.New("invalid STOCK_WATCHER_LOG_JSON")
		}
		o.JSON = b
	}
	if v := os.Getenv("STOCK_WATCHER_LOG_COLOR"); v != "" {
		o.Color = v
	}
	if o.Color != "auto" && o.Color != "force" && o.Color != "off" {
		return o, errors.New("invalid STOCK_WATCHER_LOG_COLOR")
	}
	return o, nil
}
func New(o Options) (*Logger, error) {
	if o.MaxBytes < 1 || o.MaxDays < 1 || o.Backups < 1 {
		return nil, errors.New("invalid log retention")
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if err := os.MkdirAll(o.Dir, 0700); err != nil {
		return nil, err
	}
	l := &Logger{opts: o}
	if err := l.open(); err != nil {
		return nil, err
	}
	info, err := l.file.Stat()
	if err != nil {
		l.file.Close()
		return nil, err
	}
	l.day = info.ModTime().Format("2006-01-02")
	if err := l.prune(time.Now()); err != nil {
		l.file.Close()
		return nil, err
	}
	return l, nil
}
func (l *Logger) open() error {
	f, err := os.OpenFile(filepath.Join(l.opts.Dir, "stock-watcher.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.file = f
	l.size = info.Size()
	l.day = time.Now().Format("2006-01-02")
	return nil
}
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
func (l *Logger) Info(msg string, attrs ...any)  { l.write("INFO", msg, attrs) }
func (l *Logger) Warn(msg string, attrs ...any)  { l.write("WARN", msg, attrs) }
func (l *Logger) Error(msg string, attrs ...any) { l.write("ERROR", msg, attrs) }
func (l *Logger) Debug(msg string, attrs ...any) { l.write("DEBUG", msg, attrs) }
func (l *Logger) RegisterSecret(value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, secret := range l.secrets {
		if secret == value {
			return
		}
	}
	l.secrets = append(l.secrets, value)
}
func sensitive(key string) bool {
	k := strings.ToLower(key)
	for _, word := range []string{"token", "secret", "password", "authorization", "webhook", "credential"} {
		if strings.Contains(k, word) {
			return true
		}
	}
	return false
}
func scrub(value any) any {
	if err, ok := value.(error); ok {
		return err.Error()
	}
	return scrubValue(reflect.ValueOf(value))
}

func scrubValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return scrubValue(value.Elem())
	}
	switch value.Kind() {
	case reflect.Map:
		out := map[string]any{}
		iter := value.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key().Interface())
			if sensitive(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = scrubValue(iter.Value())
			}
		}
		return out
	case reflect.Struct:
		out := map[string]any{}
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := field.Name
			if tag := field.Tag.Get("json"); tag != "" && tag != "-" {
				name = strings.Split(tag, ",")[0]
				if name == "" {
					name = field.Name
				}
			}
			if sensitive(name) {
				out[name] = "[REDACTED]"
			} else {
				out[name] = scrubValue(value.Field(i))
			}
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, value.Len())
		for i := range out {
			out[i] = scrubValue(value.Index(i))
		}
		return out
	default:
		return value.Interface()
	}
}
func caller() string {
	for depth := 2; depth < 16; depth++ {
		_, file, line, ok := runtime.Caller(depth)
		if !ok {
			break
		}
		if strings.HasSuffix(file, "/internal/logging/logging.go") || strings.Contains(file, "/log/slog/") {
			continue
		}
		for _, anchor := range []string{"/cmd/", "/internal/"} {
			if i := strings.LastIndex(file, anchor); i >= 0 {
				return fmt.Sprintf("%s:%d", file[i+1:], line)
			}
		}
		return fmt.Sprintf("%s:%d", filepath.Base(file), line)
	}
	return "unknown:0"
}
func (l *Logger) write(level, msg string, attrs []any) {
	now := time.Now()
	properties := map[string]any{}
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if !ok {
			continue
		}
		if sensitive(key) {
			properties[key] = "[REDACTED]"
		} else {
			properties[key] = scrub(attrs[i+1])
		}
	}
	prefix := fmt.Sprintf("[%s] [%s] [%s] %s", now.Format(time.RFC3339Nano), level, caller(), msg)
	var line []byte
	var err error
	if l.opts.JSON {
		properties["msg"] = prefix
		line, err = json.Marshal(properties)
	} else {
		var p []byte
		p, err = json.Marshal(properties)
		line = []byte(prefix + " " + string(p))
	}
	if err != nil {
		line = []byte(prefix + ` {"logging_error":"unsupported attribute"}`)
	}
	l.mu.Lock()
	secrets := append([]string(nil), l.secrets...)
	l.mu.Unlock()
	for _, secret := range secrets {
		line = bytes.ReplaceAll(line, []byte(secret), []byte("[REDACTED]"))
	}
	line = append(line, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	if l.size > 0 && (l.size+int64(len(line)) > l.opts.MaxBytes || now.Format("2006-01-02") != l.day) {
		if err := l.rotate(now); err != nil {
			fmt.Fprintln(os.Stderr, "stock-watcher log rotation failed")
		}
	}
	if l.file != nil {
		n, err := l.file.Write(line)
		l.size += int64(n)
		if err != nil {
			fmt.Fprintln(os.Stderr, "stock-watcher log file write failed")
		}
	}
	color := l.opts.Color == "force"
	if l.opts.Color == "auto" {
		if f, ok := l.opts.Stdout.(*os.File); ok {
			if st, e := f.Stat(); e == nil {
				color = st.Mode()&os.ModeCharDevice != 0
			}
		}
	}
	if color && !l.opts.JSON {
		fmt.Fprintf(l.opts.Stdout, "\x1b[36m%s\x1b[0m", line)
	} else {
		_, _ = l.opts.Stdout.Write(line)
	}
}
func (l *Logger) rotate(now time.Time) error {
	if err := l.file.Close(); err != nil {
		return err
	}
	l.file = nil
	path := filepath.Join(l.opts.Dir, "stock-watcher.log")
	l.seq++
	rotated := filepath.Join(l.opts.Dir, fmt.Sprintf("stock-watcher-%s-%d-%d.log", now.Format("20060102T150405.000000000"), os.Getpid(), l.seq))
	if err := os.Rename(path, rotated); err != nil {
		_ = l.open()
		return err
	}
	// Open the replacement before compression so a compression error cannot stop logging.
	if err := l.open(); err != nil {
		return err
	}
	src, err := os.Open(rotated)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(rotated+".gz", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(dst)
	_, copyErr := io.Copy(z, src)
	zipErr := z.Close()
	closeErr := dst.Close()
	if err := errors.Join(copyErr, zipErr, closeErr); err != nil {
		return err
	}
	if err := os.Remove(rotated); err != nil {
		return err
	}
	return l.prune(now)
}
func (l *Logger) prune(now time.Time) error {
	files, err := filepath.Glob(filepath.Join(l.opts.Dir, "stock-watcher-*.log*"))
	if err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	for i, path := range files {
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		if i >= l.opts.Backups || st.ModTime().Before(now.AddDate(0, 0, -l.opts.MaxDays)) {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}
