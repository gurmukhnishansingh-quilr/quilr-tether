package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gurmukhnishansingh-quilr/quilr-tether/internal/ojson"
)

const (
	maxBackups = 20
	bom        = "\xef\xbb\xbf"
)

func wrapIO(action, path string, err error) *Error {
	hint := ""
	if errors.Is(err, fs.ErrPermission) {
		hint = "check file permissions"
		if isManagedPath(path) {
			hint = adminHint()
		}
	}
	var pe *fs.PathError
	msg := err.Error()
	if errors.As(err, &pe) {
		msg = pe.Err.Error()
	}
	return ioErr(hint, "cannot %s %s: %s", action, path, msg)
}

// LoadSettings parses a settings file, preserving key order. Missing -> empty.
func LoadSettings(path string) (*ojson.Object, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ojson.NewObject(), nil
	}
	if err != nil {
		return nil, wrapIO("read", path, err)
	}
	if strings.TrimSpace(strings.TrimPrefix(string(data), bom)) == "" {
		return ojson.NewObject(), nil
	}
	obj, err := ojson.ParseObject(data)
	if err != nil {
		return nil, ioErr("fix the file by hand or run `tether restore`", "%s is not valid JSON (%v)", path, err)
	}
	return obj, nil
}

func dumpSettings(s *ojson.Object, indent string) []byte {
	b, _ := ojson.Marshal(s, indent)
	return append(b, '\n')
}

// createTemp is a seam so tests can simulate a permission failure.
var createTemp = os.CreateTemp

// AtomicWrite: temp file in the same directory -> fsync -> rename over the target.
func AtomicWrite(path string, data []byte, private bool) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return wrapIO("write", path, err)
	}
	tmp, err := createTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return wrapIO("write", path, err)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return wrapIO("write", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if private {
		mode = 0o600
	}
	_ = os.Chmod(tmpName, mode)
	if err := replaceFile(tmpName, path); err != nil {
		os.Remove(tmpName)
		return wrapIO("write", path, err)
	}
	syncDir(dir)
	return nil
}

func replaceFile(src, dst string) error {
	// Windows briefly refuses to replace a file another process has open
	// (editors, antivirus, Claude Code's file watcher); retry.
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.Rename(src, dst); err == nil || runtime.GOOS != "windows" {
			return err
		}
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	return err
}

func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// EnsureWritable fails early, before backups or anything else is written,
// when the target directory can't be written (managed scope without admin).
func EnsureWritable(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return wrapIO("write", path, err)
	}
	f, err := createTemp(dir, "."+filepath.Base(path)+".*.probe")
	if err != nil {
		return wrapIO("write", path, err)
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

// WriteSettings backs up, then atomically writes, keeping the file's indent style.
func WriteSettings(path string, s *ojson.Object, scope string) (string, error) {
	backup, err := CreateBackup(path, scope)
	if err != nil {
		return "", err
	}
	indent := "  "
	if old, err := os.ReadFile(path); err == nil {
		indent = ojson.DetectIndent(old)
	}
	return backup, AtomicWrite(path, dumpSettings(s, indent), false)
}

// --- backups ---------------------------------------------------------------

var backupRE = regexp.MustCompile(`^settings\.(\d{8}T\d{6}\.\d{6}Z)\.json$`)

const tsLayout = "20060102T150405.000000Z"

type Backup struct {
	Timestamp string `json:"timestamp"`
	Path      string `json:"path"`
	Source    string `json:"source"`
	Scope     string `json:"scope"`
	Existed   bool   `json:"existed"`
}

func (b Backup) When() string {
	t, err := time.Parse(tsLayout, b.Timestamp)
	if err != nil {
		return b.Timestamp
	}
	return t.Format(time.RFC3339)
}

type backupMeta struct {
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Existed bool   `json:"existed"`
}

func metaPath(backup string) string { return strings.TrimSuffix(backup, ".json") + ".meta" }

func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// CreateBackup copies the current file to <claude home>/backups/settings.<ts>.json.
// A missing file is recorded too (existed=false), so restoring the first
// backup removes a file that tether created.
func CreateBackup(path, scope string) (string, error) {
	dir := BackupsDir()
	var target, ts string
	for {
		ts = time.Now().UTC().Format(tsLayout)
		target = filepath.Join(dir, "settings."+ts+".json")
		if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
			break
		}
		time.Sleep(time.Microsecond)
	}
	content, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", wrapIO("read", path, err)
	}
	if !existed {
		content = []byte("{}\n")
	}
	// Backups can hold a plaintext key, so keep them owner-only.
	if err := AtomicWrite(target, content, true); err != nil {
		return "", err
	}
	meta, _ := json.MarshalIndent(backupMeta{absPath(path), scope, existed}, "", "  ")
	if err := AtomicWrite(metaPath(target), append(meta, '\n'), true); err != nil {
		return "", err
	}
	rotateBackups()
	return target, nil
}

func ListBackups() []Backup {
	entries, err := os.ReadDir(BackupsDir())
	if err != nil {
		return nil
	}
	var out []Backup
	for _, e := range entries {
		m := backupRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		p := filepath.Join(BackupsDir(), e.Name())
		meta := backupMeta{Scope: "user", Existed: true}
		if raw, err := os.ReadFile(metaPath(p)); err == nil {
			_ = json.Unmarshal(raw, &meta)
		}
		if meta.Source == "" {
			meta.Source, _ = SettingsPath("user", "")
		}
		out = append(out, Backup{m[1], p, meta.Source, meta.Scope, meta.Existed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

func rotateBackups() {
	all := ListBackups()
	for i := 0; i < len(all)-maxBackups; i++ {
		os.Remove(all[i].Path)
		os.Remove(metaPath(all[i].Path))
	}
}

// FindBackup picks by timestamp prefix, or the newest backup of source.
func FindBackup(timestamp, source string) (Backup, error) {
	all := ListBackups()
	if timestamp != "" {
		want := strings.TrimSuffix(strings.TrimPrefix(timestamp, "settings."), ".json")
		var matches []Backup
		for _, b := range all {
			if strings.HasPrefix(b.Timestamp, want) {
				matches = append(matches, b)
			}
		}
		switch len(matches) {
		case 0:
			return Backup{}, usageErr("list them with `tether restore --list`", "no backup matches %q", timestamp)
		case 1:
			return matches[0], nil
		default:
			return Backup{}, usageErr("give more digits", "%q matches %d backups", timestamp, len(matches))
		}
	}
	src := absPath(source)
	for i := len(all) - 1; i >= 0; i-- {
		if samePath(all[i].Source, src) {
			return all[i], nil
		}
	}
	return Backup{}, usageErr("", "no backups recorded for %s", source)
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// ReadBackup returns the file verbatim, or nil when it did not exist then.
func ReadBackup(b Backup) ([]byte, error) {
	if !b.Existed {
		return nil, nil
	}
	data, err := os.ReadFile(b.Path)
	if err != nil {
		return nil, wrapIO("read", b.Path, err)
	}
	return data, nil
}

func describeBackup(path string) string {
	if path == "" {
		return ""
	}
	return fmt.Sprintf("  (backup: %s)", filepath.Base(path))
}
