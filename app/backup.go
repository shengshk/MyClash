package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	backupKindManual = "manual"
	backupKindAuto   = "auto"
	backupKindMigrated = "migrated"

	backupMaxDefault  = 10
	backupCronDefault = "0 2 */7 * *"
)

// BackupMeta is one YAML snapshot in data/backups/.
type BackupMeta struct {
	ID   string // e.g. 20260718-140522.manual
	Kind string
	Time time.Time
	Path string
	Size int64
}

type BackupStore struct {
	YAMLPath string
	Dir      string
	Max      int
}

func newBackupStore(yamlPath string, max int) *BackupStore {
	if max <= 0 {
		max = backupMaxDefault
	}
	dir := filepath.Join(filepath.Dir(yamlPath), "backups")
	return &BackupStore{YAMLPath: yamlPath, Dir: dir, Max: max}
}

func (b *BackupStore) ensureDir() error {
	return os.MkdirAll(b.Dir, 0o755)
}

func (b *BackupStore) Create(kind string) (BackupMeta, error) {
	if kind == "" {
		kind = backupKindManual
	}
	data, err := os.ReadFile(b.YAMLPath)
	if err != nil {
		return BackupMeta{}, fmt.Errorf("读取配置失败: %w", err)
	}
	if err := b.ensureDir(); err != nil {
		return BackupMeta{}, err
	}
	stamp := time.Now().Format("20060102-150405")
	id := stamp + "." + kind
	path := filepath.Join(b.Dir, id+".yaml")
	if err := atomicWriteFile(path, data, 0o644); err != nil {
		return BackupMeta{}, err
	}
	b.prune()
	fi, _ := os.Stat(path)
	var size int64
	if fi != nil {
		size = fi.Size()
	}
	t, _ := time.ParseInLocation("20060102-150405", stamp, time.Local)
	return BackupMeta{ID: id, Kind: kind, Time: t, Path: path, Size: size}, nil
}

func (b *BackupStore) List() ([]BackupMeta, error) {
	if err := b.ensureDir(); err != nil {
		return nil, err
	}
	matches, err := filepath.Glob(filepath.Join(b.Dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	var out []BackupMeta
	for _, p := range matches {
		base := strings.TrimSuffix(filepath.Base(p), ".yaml")
		m, ok := parseBackupID(base, p)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Time.After(out[j].Time)
	})
	return out, nil
}

func parseBackupID(id, path string) (BackupMeta, bool) {
	// 20060102-150405.kind
	dot := strings.LastIndex(id, ".")
	if dot <= 0 || dot == len(id)-1 {
		return BackupMeta{}, false
	}
	stamp, kind := id[:dot], id[dot+1:]
	t, err := time.ParseInLocation("20060102-150405", stamp, time.Local)
	if err != nil {
		return BackupMeta{}, false
	}
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	return BackupMeta{ID: id, Kind: kind, Time: t, Path: path, Size: size}, true
}

func (b *BackupStore) Get(id string) (BackupMeta, error) {
	id = filepath.Base(id)
	path := filepath.Join(b.Dir, id+".yaml")
	m, ok := parseBackupID(id, path)
	if !ok {
		return BackupMeta{}, fmt.Errorf("备份不存在")
	}
	if _, err := os.Stat(path); err != nil {
		return BackupMeta{}, fmt.Errorf("备份不存在")
	}
	return m, nil
}

func (b *BackupStore) Delete(id string) error {
	m, err := b.Get(id)
	if err != nil {
		return err
	}
	return os.Remove(m.Path)
}

func (b *BackupStore) Restore(id string) error {
	m, err := b.Get(id)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(m.Path)
	if err != nil {
		return err
	}
	// 走写前快照，便于误操作后找回最近一次
	return (&Store{Path: b.YAMLPath}).writeWithBackup(string(data))
}

func (b *BackupStore) prune() {
	list, err := b.List()
	if err != nil || len(list) <= b.Max {
		return
	}
	for _, old := range list[b.Max:] {
		_ = os.Remove(old.Path)
	}
}

// migrateLegacyBaks moves aio.yaml.bak.* into backups/ once.
func (b *BackupStore) migrateLegacyBaks() {
	dir := filepath.Dir(b.YAMLPath)
	base := filepath.Base(b.YAMLPath)
	matches, _ := filepath.Glob(filepath.Join(dir, base+".bak.*"))
	if len(matches) == 0 {
		return
	}
	_ = b.ensureDir()
	for _, p := range matches {
		name := filepath.Base(p)
		// aio.yaml.bak.20060102-150405
		prefix := base + ".bak."
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		stamp := strings.TrimPrefix(name, prefix)
		if _, err := time.ParseInLocation("20060102-150405", stamp, time.Local); err != nil {
			continue
		}
		dest := filepath.Join(b.Dir, stamp+"."+backupKindMigrated+".yaml")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if _, err := os.Stat(dest); err == nil {
			_ = os.Remove(p)
			continue
		}
		if err := atomicWriteFile(dest, data, 0o644); err != nil {
			continue
		}
		_ = os.Remove(p)
	}
	b.prune()
}

func kindLabel(kind string) string {
	switch kind {
	case backupKindManual:
		return "手动"
	case backupKindAuto:
		return "定时"
	case backupKindMigrated:
		return "迁移"
	default:
		return kind
	}
}

func parseBackupCron(spec string) (min, hour, everyDays int, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = backupCronDefault
	}
	f := strings.Fields(spec)
	if len(f) != 5 {
		return 0, 0, 0, fmt.Errorf("BACKUP_CRON 需要 5 段，例如 %q", backupCronDefault)
	}
	min, err = strconv.Atoi(f[0])
	if err != nil || min < 0 || min > 59 {
		return 0, 0, 0, fmt.Errorf("BACKUP_CRON 分钟无效")
	}
	hour, err = strconv.Atoi(f[1])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, 0, fmt.Errorf("BACKUP_CRON 小时无效")
	}
	switch {
	case f[2] == "*":
		everyDays = 1
	case strings.HasPrefix(f[2], "*/"):
		everyDays, err = strconv.Atoi(strings.TrimPrefix(f[2], "*/"))
		if err != nil || everyDays <= 0 {
			return 0, 0, 0, fmt.Errorf("BACKUP_CRON 间隔天数无效")
		}
	default:
		return 0, 0, 0, fmt.Errorf("BACKUP_CRON 日期段仅支持 * 或 */N")
	}
	if f[3] != "*" || f[4] != "*" {
		return 0, 0, 0, fmt.Errorf("BACKUP_CRON 月/周请使用 *")
	}
	return min, hour, everyDays, nil
}

func nextBackupTime(now time.Time, min, hour, everyDays int, lastAuto time.Time) time.Time {
	loc := now.Location()
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, loc)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	if !lastAuto.IsZero() {
		earliest := lastAuto.In(loc).AddDate(0, 0, everyDays)
		earliest = time.Date(earliest.Year(), earliest.Month(), earliest.Day(), hour, min, 0, 0, loc)
		if earliest.After(t) {
			t = earliest
		}
	}
	return t
}

func (b *BackupStore) lastAutoTime() time.Time {
	list, err := b.List()
	if err != nil {
		return time.Time{}
	}
	for _, m := range list {
		if m.Kind == backupKindAuto {
			return m.Time
		}
	}
	return time.Time{}
}

func loadBackupMax() int {
	s := strings.TrimSpace(os.Getenv("BACKUP_MAX"))
	if s == "" {
		return backupMaxDefault
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return backupMaxDefault
	}
	return n
}
