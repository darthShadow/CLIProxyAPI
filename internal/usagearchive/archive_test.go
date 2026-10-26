package usagearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func archiveSetup(t *testing.T) (*archivePlugin, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	t.Setenv("CPA_USAGE_DIR", dir)
	old := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(old) })
	return &archivePlugin{now: func() time.Time { return fixtureRecord().RequestedAt }}, dir
}
func archivePath(dir string, at time.Time) string {
	return filepath.Join(dir, "usage-"+at.Format("2006-01-02")+".jsonl")
}
func readArchive(t *testing.T, dir string) []line {
	t.Helper()
	b, e := os.ReadFile(archivePath(dir, fixtureRecord().RequestedAt))
	if e != nil {
		t.Fatal(e)
	}
	var rows []line
	for _, s := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var l line
		if e := json.Unmarshal([]byte(s), &l); e != nil {
			t.Fatal(e)
		}
		rows = append(rows, l)
	}
	return rows
}
func TestHandleUsageWritesPrivateDailyFile(t *testing.T) {
	p, dir := archiveSetup(t)
	p.HandleUsage(context.Background(), fixtureRecord())
	rows := readArchive(t, dir)
	if len(rows) != 1 || rows[0].Input != 10 {
		t.Fatalf("rows=%+v", rows)
	}
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{{dir, 0700}, {archivePath(dir, fixtureRecord().RequestedAt), 0600}} {
		st, e := os.Stat(tc.path)
		if e != nil || st.Mode().Perm() != tc.mode {
			t.Fatalf("stat %s: %v %v", tc.path, st, e)
		}
	}
}
func TestHandleUsageCountsLossAndRecovers(t *testing.T) {
	p, dir := archiveSetup(t)
	if e := os.WriteFile(dir, []byte("block"), 0600); e != nil {
		t.Fatal(e)
	}
	var b bytes.Buffer
	logger := log.StandardLogger()
	old := logger.Out
	logger.SetOutput(&b)
	t.Cleanup(func() { logger.SetOutput(old) })
	p.HandleUsage(context.Background(), fixtureRecord())
	if p.lost != 1 {
		t.Errorf("loss=%d want=1", p.lost)
	}
	p.HandleUsage(context.Background(), fixtureRecord())
	if strings.Count(b.String(), "append failed") != 1 {
		t.Errorf("warnings=%q", b.String())
	}
	if e := os.Remove(dir); e != nil {
		t.Fatal(e)
	}
	p.HandleUsage(context.Background(), fixtureRecord())
	if len(readArchive(t, dir)) != 1 || p.lost != 2 {
		t.Fatal("recovery did not preserve count and write record")
	}
}

var _ coreusage.Plugin = (*archivePlugin)(nil)

func TestHandleUsageOffWhenStatisticsOff(t *testing.T) {
	p, dir := archiveSetup(t)
	redisqueue.SetUsageStatisticsEnabled(false)
	p.HandleUsage(context.Background(), fixtureRecord())
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Errorf("file created while statistics off: %v", e)
	}
	if p.lost != 0 {
		t.Errorf("loss=%d", p.lost)
	}
}
func TestHandleUsageOffWhenDirUnset(t *testing.T) {
	p, dir := archiveSetup(t)
	t.Setenv("CPA_USAGE_DIR", "")
	p.HandleUsage(context.Background(), fixtureRecord())
	if p.lost != 0 {
		t.Errorf("loss=%d want=0 for unset dir", p.lost)
	}
	tmp := filepath.Dir(dir)
	entries, e := os.ReadDir(tmp)
	if e != nil || len(entries) != 0 {
		t.Fatalf("tmp not empty: %v %v", entries, e)
	}
}
func TestHandleUsagePrunesOnDateChange(t *testing.T) {
	p, dir := archiveSetup(t)
	now := p.now()
	os.MkdirAll(dir, 0700)
	for _, day := range []int{-31, -30, 0} {
		if e := os.WriteFile(archivePath(dir, now.AddDate(0, 0, day)), nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	note := filepath.Join(dir, "notes.txt")
	os.WriteFile(note, nil, 0600)
	p.HandleUsage(context.Background(), fixtureRecord())
	if _, e := os.Stat(archivePath(dir, now.AddDate(0, 0, -31))); !os.IsNotExist(e) {
		t.Error("old file remains")
	}
	if _, e := os.Stat(archivePath(dir, now.AddDate(0, 0, -30))); e != nil {
		t.Error("30-day file removed")
	}
	p.now = func() time.Time { return now.AddDate(0, 0, 1) }
	p.HandleUsage(context.Background(), fixtureRecord())
	if _, e := os.Stat(archivePath(dir, now.AddDate(0, 0, -30))); !os.IsNotExist(e) {
		t.Error("old file remains after date change")
	}
	if _, e := os.Stat(note); e != nil {
		t.Error("notes removed")
	}
}
func TestHandleUsageStartsOnFreshLine(t *testing.T) {
	p, dir := archiveSetup(t)
	os.MkdirAll(dir, 0700)
	path := archivePath(dir, p.now())
	os.WriteFile(path, []byte(`{"torn":`), 0600)
	p.HandleUsage(context.Background(), fixtureRecord())
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	parts := strings.Split(string(b), "\n")
	if len(parts) != 3 || parts[0] != `{"torn":` {
		t.Fatalf("record joined torn line: %q", b)
	}
	var l line
	if e := json.Unmarshal([]byte(parts[1]), &l); e != nil || l.Input != 10 {
		t.Fatalf("fresh record=%+v err=%v", l, e)
	}
}
func TestHandleUsageLogsRecovery(t *testing.T) {
	p, dir := archiveSetup(t)
	os.WriteFile(dir, []byte("block"), 0600)
	var b bytes.Buffer
	logger := log.StandardLogger()
	old := logger.Out
	logger.SetOutput(&b)
	t.Cleanup(func() { logger.SetOutput(old) })
	p.HandleUsage(context.Background(), fixtureRecord())
	os.Remove(dir)
	p.HandleUsage(context.Background(), fixtureRecord())
	if !strings.Contains(b.String(), "recovered") || !strings.Contains(b.String(), "1") {
		t.Fatalf("no recovery log: %s", b.String())
	}
}

type archiveProbe chan struct{}

func (p archiveProbe) HandleUsage(context.Context, coreusage.Record) { p <- struct{}{} }
func TestArchiveRecordsThroughDefaultManager(t *testing.T) {
	_, dir := archiveSetup(t)
	probe := make(archiveProbe, 1)
	coreusage.RegisterPlugin(probe)
	coreusage.StartDefault(context.Background())
	ctx := logging.WithClientRequestMetadata(context.Background(), logging.ClientRequestMetadata{UserAgent: "claude-cli/2.1.310 (external, cli)"})
	coreusage.PublishRecord(ctx, fixtureRecord())
	select {
	case <-probe:
	case <-time.After(5 * time.Second):
		t.Fatal("probe not reached")
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 1 {
		t.Fatalf("no archive after probe: %v %v", entries, e)
	}
	b, e := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if e != nil {
		t.Fatal(e)
	}
	var count int
	for _, s := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(s) != "" {
			var l line
			if e := json.Unmarshal([]byte(s), &l); e != nil {
				t.Fatal(e)
			}
			if l.Client != "claude-code-cli" {
				t.Errorf("client=%q, want claude-code-cli", l.Client)
			}
			if !strings.HasPrefix(s, `{"ts":`) {
				t.Errorf("line starts %.20q, want the old leading ts key", s)
			}
			count++
		}
	}
	if count != 1 {
		t.Errorf("lines=%d want=1", count)
	}
}

func TestHandleUsagePruneFailureKeepsRecording(t *testing.T) {
	p, dir := archiveSetup(t)
	now := p.now()
	expired := archivePath(dir, now.AddDate(0, 0, -31))
	if err := os.MkdirAll(expired, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(expired, "keep"), []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	removable := archivePath(dir, now.AddDate(0, 0, -32))
	if err := os.WriteFile(removable, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	logger := log.StandardLogger()
	old := logger.Out
	logger.SetOutput(&b)
	t.Cleanup(func() { logger.SetOutput(old) })
	p.HandleUsage(context.Background(), fixtureRecord())
	if p.lost != 0 {
		t.Errorf("prune failure raised append losses: %d", p.lost)
	}
	if _, err := os.Stat(archivePath(dir, now)); err != nil {
		t.Errorf("today's record missing after prune failure: %v", err)
	} else if rows := readArchive(t, dir); len(rows) != 1 || rows[0].Input != 10 {
		t.Errorf("records=%+v", rows)
	}
	if _, err := os.Stat(removable); !os.IsNotExist(err) {
		t.Errorf("other expired file not removed: %v", err)
	}
	if !strings.Contains(b.String(), filepath.Base(expired)) || !strings.Contains(b.String(), "prune") {
		t.Errorf("missing prune filename log: %s", b.String())
	}
	if strings.Contains(b.String(), "a@example.test") {
		t.Errorf("record data in prune log: %s", b.String())
	}
}
