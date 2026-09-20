package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/soraincloud/srics-next/internal/library"
)

type backupRecord struct {
	Repository   string `json:"repository,omitempty"`
	Initialized  bool   `json:"initialized,omitempty"`
	Status       string `json:"status"`
	AttemptedAt  string `json:"attemptedAt,omitempty"`
	FinishedAt   string `json:"finishedAt,omitempty"`
	SavedAt      string `json:"savedAt,omitempty"`
	VerifiedAt   string `json:"verifiedAt,omitempty"`
	Snapshot     string `json:"snapshot,omitempty"`
	ReadVerified bool   `json:"readVerified"`
	Error        string `json:"error,omitempty"`
}

func (a *LibraryAPI) readBackupRecord() (backupRecord, error) {
	data, err := a.store.Setting("backup")
	var record backupRecord
	if err == nil && len(data) > 0 {
		err = json.Unmarshal(data, &record)
	}
	// Status from a different destination must not claim this one is backed up.
	if record.Repository != "" && record.Repository != a.backupRepositoryID() {
		record = backupRecord{}
	}
	return record, err
}
func (a *LibraryAPI) backupRepositoryID() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(a.backup.Repository)))
}
func (a *LibraryAPI) saveBackupRecord(record backupRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return a.store.SetSetting("backup", data)
}

// nextBackup uses local calendar days, so daylight-saving changes do not turn
// a daily wall-clock time into a drifting 24-hour interval. Missed days coalesce.
func nextBackup(now time.Time, at string, record backupRecord) time.Time {
	clock, err := time.Parse("15:04", at)
	if err != nil || len(at) != 5 {
		return time.Time{}
	}
	due := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
	if due.After(now) {
		due = due.AddDate(0, 0, -1)
	}
	saved, _ := time.Parse(time.RFC3339Nano, record.SavedAt)
	if !saved.Before(due) {
		return due.AddDate(0, 0, 1)
	}
	if record.Status == "failed" || record.Status == "running" {
		attempt, _ := time.Parse(time.RFC3339Nano, record.FinishedAt)
		if attempt.IsZero() {
			attempt, _ = time.Parse(time.RFC3339Nano, record.AttemptedAt)
		}
		if retry := attempt.Add(time.Hour); retry.After(due) {
			due = retry
		}
	}
	if due.Before(now) {
		return now
	}
	return due
}

// Call once before serving requests; the scheduler never overlaps manual work.
func (s *Server) StartBackupSchedule(at string) {
	a := s.library
	if a == nil || at == "" || a.backup.Repository == "" {
		return
	}
	if _, err := time.Parse("15:04", at); err != nil || len(at) != 5 {
		return
	}
	a.backupDailyAt = at
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		timer := time.NewTicker(30 * time.Second)
		defer timer.Stop()
		for {
			now := time.Now()
			record, err := a.readBackupRecord()
			if err == nil && !nextBackup(now, at, record).After(now) {
				_ = a.startBackup()
			}
			select {
			case <-a.ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}

func (a *LibraryAPI) backupStatus() (any, error) {
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	record, err := a.readBackupRecord()
	if err != nil {
		return nil, err
	}
	if a.backupStateError != nil {
		return nil, a.backupStateError
	}
	var next string
	if a.backupDailyAt != "" && !a.backupActive {
		next = nextBackup(time.Now(), a.backupDailyAt, record).Format(time.RFC3339)
	}
	if record.Status == "running" && !a.backupActive {
		record.Status, record.Error = "failed", "上次备份因服务停止而中断，尚未确认成功"
	}
	zone, _ := time.Now().Zone()
	return map[string]any{"configured": a.backup.Repository != "", "running": a.backupActive, "last": record, "dailyAt": a.backupDailyAt, "timeZone": zone, "nextRunAt": next}, nil
}

func (a *LibraryAPI) startBackup() error {
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	if a.backupActive {
		return library.ErrConflict
	}
	if a.backup.Repository == "" {
		return errors.New("请在本机程序中配置备份目录与独立备份口令")
	}
	if a.ctx.Err() != nil {
		return errors.New("服务正在停止")
	}
	record, err := a.readBackupRecord()
	if err != nil {
		return err
	}
	record.Repository = a.backupRepositoryID()
	record.AttemptedAt = time.Now().UTC().Format(time.RFC3339Nano)
	record.FinishedAt, record.Error, record.Status = "", "", "running"
	if err = a.saveBackupRecord(record); err != nil {
		return err
	}
	a.backupStateError = nil
	a.backupActive = true
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Hour)
		defer cancel()
		stage := filepath.Join(a.store.Root, "staging", "backup-"+library.NewID())
		defer os.RemoveAll(stage)
		err := a.store.Snapshot(ctx, stage)
		if err == nil {
			pinned, e := library.Open(stage)
			if e != nil {
				err = e
			} else {
				err = pinned.Verify(ctx)
				closeErr := pinned.Close()
				if err == nil {
					err = closeErr
				}
			}
		}
		if err == nil {
			if _, e := os.Stat(filepath.Join(a.backup.Repository, "config")); e != nil {
				if !os.IsNotExist(e) {
					err = e
				} else if record.Initialized || record.Snapshot != "" {
					err = errors.New("已有备份仓库缺失")
				} else {
					// Missing removable drive must not create a replacement hierarchy.
					parent, parentErr := os.Stat(filepath.Dir(a.backup.Repository))
					entries, readErr := os.ReadDir(a.backup.Repository)
					if parentErr != nil || !parent.IsDir() || !(os.IsNotExist(readErr) || (readErr == nil && len(entries) == 0)) {
						err = errors.New("备份目标不可用或非空")
					} else {
						err = a.backup.Init(ctx)
					}
				}
			}
			if err == nil {
				record.Initialized = true
				err = a.saveBackupRecord(record)
			}
		}
		id := ""
		if err == nil {
			id, err = a.backup.BackupLibrary(ctx, stage)
		}
		if err == nil {
			err = a.backup.Check(ctx)
		}
		record.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		record.Status = "passed"
		if err != nil {
			record.Status, record.Error = "failed", "备份未完成，请检查备份盘、口令文件及资料盘"
		} else {
			record.Snapshot, record.SavedAt, record.VerifiedAt, record.ReadVerified = id, record.AttemptedAt, record.FinishedAt, true
		}
		a.backupMu.Lock()
		defer a.backupMu.Unlock()
		if err := a.saveBackupRecord(record); err != nil {
			a.backupStateError = errors.New("无法记录备份结果，请检查资料盘；本次未确认成功")
		}
		a.backupActive = false
	}()
	return nil
}
