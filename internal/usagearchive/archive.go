package usagearchive

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type archivePlugin struct {
	mu          sync.Mutex
	now         func() time.Time
	lost        uint64
	lastFailure time.Time
	failing     bool
	fresh       bool
	pruneDay    string
}

var recorder = &archivePlugin{}

func (p *archivePlugin) HandleUsage(ctx context.Context, r coreusage.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !redisqueue.UsageStatisticsEnabled() || os.Getenv("CPA_USAGE_DIR") == "" {
		return
	}
	l, ok := project(r, helps.UsageClient(logging.GetClientRequestMetadata(ctx).UserAgent))
	if !ok {
		return
	}
	now := time.Now()
	if p.now != nil {
		now = p.now()
	}
	dir := os.Getenv("CPA_USAGE_DIR")
	if p.pruneDay == "" {
		p.fresh = true
	}
	err := p.append(dir, now, l)
	if err != nil {
		p.lost++
		p.lastFailure = now
		if !p.failing {
			log.Warnf("usage archive: append failed: %v", err)
		}
		p.fresh = true
		p.failing = true
		return
	}
	if p.failing {
		log.Infof("usage archive: appends recovered; %d lost since start", p.lost)
	}
	p.fresh = false
	p.failing = false
}
func (p *archivePlugin) append(dir string, now time.Time, l line) error {
	if errMkdirAll := os.MkdirAll(dir, 0700); errMkdirAll != nil {
		return errMkdirAll
	}
	day := now.Format("2006-01-02")
	if p.pruneDay != day {
		entries, errReadDir := os.ReadDir(dir)
		if errReadDir != nil {
			log.Warnf("usage archive: prune %s: %v", dir, errReadDir)
		}
		cutoff := now.AddDate(0, 0, -30).Format("2006-01-02")
		for _, entry := range entries {
			name := entry.Name()
			if len(name) != 22 || name[:6] != "usage-" || name[16:] != ".jsonl" {
				continue
			}
			date := name[6:16]
			if _, errParse := time.Parse("2006-01-02", date); errParse == nil && date < cutoff {
				if errRemove := os.Remove(filepath.Join(dir, name)); errRemove != nil {
					log.Warnf("usage archive: prune %s: %v", name, errRemove)
				}
			}
		}
		p.pruneDay = day
	}
	b, errMarshal := json.Marshal(l)
	if errMarshal != nil {
		return errMarshal
	}
	b = append(b, '\n')
	if p.fresh {
		b = append([]byte{'\n'}, b...)
	}
	f, errOpenFile := os.OpenFile(filepath.Join(dir, "usage-"+now.Format("2006-01-02")+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if errOpenFile != nil {
		return errOpenFile
	}
	n, errWrite := f.Write(b)
	errClose := f.Close()
	if errClose != nil {
		log.Warnf("usage archive: close failed: %v", errClose)
	}
	if errWrite != nil {
		return errWrite
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	return errClose
}

// Losses returns failed appends since start and the last failure time.
func Losses() (uint64, time.Time) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.lost, recorder.lastFailure
}
func init() { coreusage.RegisterNamedPlugin("usage-archive", recorder) }
