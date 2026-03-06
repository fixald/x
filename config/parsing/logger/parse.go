package logger

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	xlogger "github.com/go-gost/x/logger"
	"github.com/go-gost/x/registry"
	"gopkg.in/natefinch/lumberjack.v2"
)

func ParseLogger(cfg *config.LoggerConfig) logger.Logger {
	if cfg == nil || cfg.Log == nil {
		return nil
	}
	opts := []xlogger.Option{
		xlogger.NameOption(cfg.Name),
		xlogger.FormatOption(logger.LogFormat(cfg.Log.Format)),
		xlogger.LevelOption(logger.LogLevel(cfg.Log.Level)),
	}

	var out io.Writer = os.Stderr
	switch cfg.Log.Output {
	case "none", "null":
		out = io.Discard
	case "stdout":
		out = os.Stdout
	case "stderr", "":
		out = os.Stderr
	default:
		if cfg.Log.Rotation != nil && (cfg.Log.Rotation.Interval == "hourly" || cfg.Log.Rotation.Interval == "1h") {
			out = NewHourlyWriter(cfg.Log.Output, cfg.Log.Rotation.LocalTime)
		} else if cfg.Log.Rotation != nil {
			out = &lumberjack.Logger{
				Filename:   cfg.Log.Output,
				MaxSize:    cfg.Log.Rotation.MaxSize,
				MaxAge:     cfg.Log.Rotation.MaxAge,
				MaxBackups: cfg.Log.Rotation.MaxBackups,
				LocalTime:  cfg.Log.Rotation.LocalTime,
				Compress:   cfg.Log.Rotation.Compress,
			}
		} else {
			os.MkdirAll(filepath.Dir(cfg.Log.Output), 0755)
			f, err := os.OpenFile(cfg.Log.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
			if err != nil {
				logger.Default().Warn(err)
			} else {
				out = f
			}
		}
	}
	opts = append(opts, xlogger.OutputOption(out))

	return xlogger.NewLogger(opts...)
}

// HourlyWriter 按小时轮转：文件名形如 base.2006010215.log（如 2026030711），每小时一个文件；实现 io.WriteCloser 供 recorder 使用
type HourlyWriter struct {
	basePath  string // 不含时间戳和扩展名，如 /var/log/gost/app
	ext       string // 如 .log
	localTime bool
	mu        sync.Mutex
	file      *os.File
	rotateAt  time.Time // 当前文件对应的小时（整点）
}

// NewHourlyWriter 创建按小时轮转的 writer，path 为完整文件路径（如 /var/log/gost/rec.log），localTime 为是否使用本地时区
func NewHourlyWriter(path string, localTime bool) *HourlyWriter {
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0755)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext == "" {
		ext = ".log"
	}
	baseName := base[:len(base)-len(ext)]
	basePath := filepath.Join(dir, baseName)
	w := &HourlyWriter{
		basePath:  basePath,
		ext:       ext,
		localTime: localTime,
	}
	w.rotateLocked()
	return w
}

func (w *HourlyWriter) filename(t time.Time) string {
	if w.localTime {
		t = t.Local()
	}
	return w.basePath + "." + t.Format("2006010215") + w.ext
}

func (w *HourlyWriter) rotateLocked() {
	now := time.Now()
	if w.localTime {
		now = now.Local()
	}
	slot := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	w.rotateAt = slot
	fpath := w.filename(now)
	f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		if logger.Default() != nil {
			logger.Default().Warn(err)
		}
		return
	}
	if w.file != nil {
		w.file.Close()
	}
	w.file = f
}

// Write 实现 io.Writer
func (w *HourlyWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if w.localTime {
		now = now.Local()
	}
	slot := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	if slot != w.rotateAt || w.file == nil {
		w.rotateLocked()
	}
	if w.file == nil {
		return 0, nil
	}
	return w.file.Write(p)
}

// Close 实现 io.Closer，供 recorder 使用
func (w *HourlyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

func List(name string, names ...string) []logger.Logger {
	var loggers []logger.Logger
	if adm := registry.LoggerRegistry().Get(name); adm != nil {
		loggers = append(loggers, adm)
	}
	for _, s := range names {
		if lg := registry.LoggerRegistry().Get(s); lg != nil {
			loggers = append(loggers, lg)
		}
	}

	return loggers
}
