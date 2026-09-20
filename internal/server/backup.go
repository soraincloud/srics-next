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

	"github.com/soraincloud/srics-next/internal/backup"
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

func targetName(target []string) string {
	if len(target) > 0 && target[0] == "cloud" {
		return "cloud"
	}
	return "local"
}
func (a *LibraryAPI) backupClient(target string) backup.Client {
	if target == "cloud" {
		return a.cloud
	}
	return a.backup
}
func backupSetting(target string) string {
	if target == "cloud" {
		return "backup-cloud"
	}
	return "backup"
}
func (s *Server) EnableCloudBackup(c backup.Client) { s.library.cloud = c }

func (a *LibraryAPI) readBackupRecord(target ...string) (backupRecord, error) {
	data, err := a.store.Setting(backupSetting(targetName(target)))
	var record backupRecord
	if err == nil && len(data) > 0 {
		err = json.Unmarshal(data, &record)
	}
	// Status from a different destination must not claim this one is backed up.
	if record.Repository != "" && record.Repository != a.backupRepositoryID(target...) {
		record = backupRecord{}
	}
	return record, err
}
func (a *LibraryAPI) backupRepositoryID(target ...string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(a.backupClient(targetName(target)).Repository)))
}
func (a *LibraryAPI) saveBackupRecord(record backupRecord, target ...string) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return a.store.SetSetting(backupSetting(targetName(target)), data)
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
	if a == nil || at == "" || (a.backup.Repository == "" && a.cloud.Repository == "") {
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
			for _, target := range []string{"local", "cloud"} {
				if a.backupClient(target).Repository == "" {
					continue
				}
				record, err := a.readBackupRecord(target)
				if err == nil && !nextBackup(now, at, record).After(now) {
					_ = a.startBackup(target)
				}
			}
			select {
			case <-a.ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}

func (a *LibraryAPI) backupStatus(target ...string) (any, error) {
	name := targetName(target)
	client := a.backupClient(name)
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	record, err := a.readBackupRecord(target...)
	if err != nil {
		return nil, err
	}
	if err := a.backupStateErrors[name]; err != nil {
		return nil, err
	}
	var next string
	if a.backupDailyAt != "" && !a.backupActive && client.Repository != "" {
		next = nextBackup(time.Now(), a.backupDailyAt, record).Format(time.RFC3339)
	}
	if record.Status == "running" && !(a.backupActive && a.backupActiveTarget == name) {
		record.Status, record.Error = "failed", "上次备份因服务停止而中断，尚未确认成功"
	}
	zone, _ := time.Now().Zone()
	return map[string]any{"target": name, "destination": client.Repository, "configured": client.Repository != "", "running": a.backupActive && a.backupActiveTarget == name, "busy": a.backupActive, "last": record, "dailyAt": a.backupDailyAt, "timeZone": zone, "nextRunAt": next}, nil
}

func (a *LibraryAPI) startBackup(target ...string) error {
	name := targetName(target)
	client := a.backupClient(name)
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	if a.backupActive {
		return library.ErrConflict
	}
	if client.Repository == "" {
		return errors.New("请在本机程序中配置备份目录与独立备份口令")
	}
	if a.ctx.Err() != nil {
		return errors.New("服务正在停止")
	}
	record, err := a.readBackupRecord(target...)
	if err != nil {
		return err
	}
	record.Repository = a.backupRepositoryID(target...)
	record.AttemptedAt = time.Now().UTC().Format(time.RFC3339Nano)
	record.FinishedAt, record.Error, record.Status = "", "", "running"
	if err = a.saveBackupRecord(record, target...); err != nil {
		return err
	}
	if a.backupStateErrors == nil {
		a.backupStateErrors = map[string]error{}
	}
	delete(a.backupStateErrors, name)
	a.backupActive = true
	a.backupActiveTarget = name
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
		if err == nil && client.S3 != nil {
			err = client.PrepareCloud(ctx, record.Initialized || record.Snapshot != "")
			if err == nil {
				record.Initialized = true
				err = a.saveBackupRecord(record, target...)
			}
		}
		if err == nil && client.S3 == nil {
			if _, e := os.Stat(filepath.Join(client.Repository, "config")); e != nil {
				if !os.IsNotExist(e) {
					err = e
				} else if record.Initialized || record.Snapshot != "" {
					err = errors.New("已有备份仓库缺失")
				} else {
					// Missing removable drive must not create a replacement hierarchy.
					parent, parentErr := os.Stat(filepath.Dir(client.Repository))
					entries, readErr := os.ReadDir(client.Repository)
					if parentErr != nil || !parent.IsDir() || !(os.IsNotExist(readErr) || (readErr == nil && len(entries) == 0)) {
						err = errors.New("备份目标不可用或非空")
					} else {
						err = client.Init(ctx)
					}
				}
			}
			if err == nil {
				record.Initialized = true
				err = a.saveBackupRecord(record, target...)
			}
		}
		id := ""
		if err == nil {
			id, err = client.BackupLibrary(ctx, stage)
		}
		if err == nil {
			err = client.Check(ctx)
		}
		record.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		record.Status = "passed"
		if err != nil {
			record.Status, record.Error = "failed", "备份未完成，请检查目标连接、权限、口令文件及资料盘"
		} else {
			record.Snapshot, record.SavedAt, record.VerifiedAt, record.ReadVerified = id, record.AttemptedAt, record.FinishedAt, true
		}
		a.backupMu.Lock()
		defer a.backupMu.Unlock()
		if err := a.saveBackupRecord(record, target...); err != nil {
			a.backupStateErrors[name] = errors.New("无法记录备份结果，请检查资料盘；本次未确认成功")
		}
		a.backupActive = false
	}()
	return nil
}
