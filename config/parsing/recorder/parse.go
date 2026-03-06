package recorder

import (
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/core/recorder"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/internal/plugin"
	xrecorder "github.com/go-gost/x/recorder"
	recorder_plugin "github.com/go-gost/x/recorder/plugin"
	"gopkg.in/natefinch/lumberjack.v2"
)

type discardCloser struct{}

func (discardCloser) Write(p []byte) (n int, err error) { return len(p), nil }
func (discardCloser) Close() error                      { return nil }

func ParseRecorder(cfg *config.RecorderConfig) (r recorder.Recorder) {
	if cfg == nil {
		return nil
	}

	if cfg.Plugin != nil {
		var tlsCfg *tls.Config
		if cfg.Plugin.TLS != nil {
			tlsCfg = &tls.Config{
				ServerName:         cfg.Plugin.TLS.ServerName,
				InsecureSkipVerify: !cfg.Plugin.TLS.Secure,
			}
		}
		switch strings.ToLower(cfg.Plugin.Type) {
		case "http":
			return recorder_plugin.NewHTTPPlugin(
				cfg.Name, cfg.Plugin.Addr,
				plugin.TLSConfigOption(tlsCfg),
				plugin.TimeoutOption(cfg.Plugin.Timeout),
			)
		default:
			return recorder_plugin.NewGRPCPlugin(
				cfg.Name, cfg.Plugin.Addr,
				plugin.TokenOption(cfg.Plugin.Token),
				plugin.TLSConfigOption(tlsCfg),
			)
		}
	}

	if cfg.File != nil && cfg.File.Path != "" {
		var out io.WriteCloser = discardCloser{}

		if cfg.File.Rotation != nil && (cfg.File.Rotation.Interval == "hourly" || cfg.File.Rotation.Interval == "1h") {
			out = newHourlyWriteCloser(cfg.File.Path, cfg.File.Rotation.LocalTime)
		} else if cfg.File.Rotation != nil {
			out = &lumberjack.Logger{
				Filename:   cfg.File.Path,
				MaxSize:    cfg.File.Rotation.MaxSize,
				MaxAge:     cfg.File.Rotation.MaxAge,
				MaxBackups: cfg.File.Rotation.MaxBackups,
				LocalTime:  cfg.File.Rotation.LocalTime,
				Compress:   cfg.File.Rotation.Compress,
			}
		} else {
			os.MkdirAll(filepath.Dir(cfg.File.Path), 0755)
			f, err := os.OpenFile(cfg.File.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
			if err != nil {
				logger.Default().Warn(err)
			} else {
				out = f
			}
		}

		return xrecorder.FileRecorder(out,
			xrecorder.RecorderFileRecorderOption(cfg.Name),
			xrecorder.SepFileRecorderOption(cfg.File.Sep),
		)
	}

	if cfg.TCP != nil && cfg.TCP.Addr != "" {
		return xrecorder.TCPRecorder(cfg.TCP.Addr,
			xrecorder.RecorderTCPRecorderOption(cfg.Name),
			xrecorder.TimeoutTCPRecorderOption(cfg.TCP.Timeout),
		)
	}

	if cfg.HTTP != nil && cfg.HTTP.URL != "" {
		h := http.Header{}
		for k, v := range cfg.HTTP.Header {
			h.Add(k, v)
		}
		return xrecorder.HTTPRecorder(cfg.HTTP.URL,
			xrecorder.RecorderHTTPRecorderOption(cfg.Name),
			xrecorder.TimeoutHTTPRecorderOption(cfg.HTTP.Timeout),
			xrecorder.HeaderHTTPRecorderOption(h),
		)
	}

	if cfg.Redis != nil &&
		cfg.Redis.Addr != "" &&
		cfg.Redis.Key != "" {
		switch cfg.Redis.Type {
		case "list": // redis list
			return xrecorder.RedisListRecorder(cfg.Redis.Addr,
				xrecorder.RecorderRedisRecorderOption(cfg.Name),
				xrecorder.DBRedisRecorderOption(cfg.Redis.DB),
				xrecorder.KeyRedisRecorderOption(cfg.Redis.Key),
				xrecorder.UsernameRedisRecorderOption(cfg.Redis.Username),
				xrecorder.PasswordRedisRecorderOption(cfg.Redis.Password),
			)
		case "sset": // sorted set
			return xrecorder.RedisSortedSetRecorder(cfg.Redis.Addr,
				xrecorder.RecorderRedisRecorderOption(cfg.Name),
				xrecorder.DBRedisRecorderOption(cfg.Redis.DB),
				xrecorder.KeyRedisRecorderOption(cfg.Redis.Key),
				xrecorder.UsernameRedisRecorderOption(cfg.Redis.Username),
				xrecorder.PasswordRedisRecorderOption(cfg.Redis.Password),
			)
		default: // redis set
			return xrecorder.RedisSetRecorder(cfg.Redis.Addr,
				xrecorder.RecorderRedisRecorderOption(cfg.Name),
				xrecorder.DBRedisRecorderOption(cfg.Redis.DB),
				xrecorder.KeyRedisRecorderOption(cfg.Redis.Key),
				xrecorder.UsernameRedisRecorderOption(cfg.Redis.Username),
				xrecorder.PasswordRedisRecorderOption(cfg.Redis.Password),
			)
		}
	}

	return
}

// hourlyWriteCloser 按小时轮转，实现 io.WriteCloser，供 file recorder 使用
type hourlyWriteCloser struct {
	basePath  string
	ext       string
	localTime bool
	mu        sync.Mutex
	file      *os.File
	rotateAt  time.Time
}

func newHourlyWriteCloser(path string, localTime bool) io.WriteCloser {
	dir := filepath.Dir(path)
	os.MkdirAll(dir, 0755)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	if ext == "" {
		ext = ".log"
	}
	baseName := base[:len(base)-len(ext)]
	basePath := filepath.Join(dir, baseName)
	w := &hourlyWriteCloser{
		basePath:  basePath,
		ext:       ext,
		localTime: localTime,
	}
	w.rotateLocked()
	return w
}

func (w *hourlyWriteCloser) filename(t time.Time) string {
	if w.localTime {
		t = t.Local()
	}
	return w.basePath + "." + t.Format("2006010215") + w.ext
}

func (w *hourlyWriteCloser) rotateLocked() {
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

func (w *hourlyWriteCloser) Write(p []byte) (n int, err error) {
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

func (w *hourlyWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}
