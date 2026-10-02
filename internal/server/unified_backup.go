package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"time"
)

func (s *Server) EnableUnifiedBackup(u backup.Unified) { s.library.unified = &u }
func (a *LibraryAPI) readUnifiedRecord() (backupRecord, error) {
	var r backupRecord
	b, err := a.store.Setting("backup-unified")
	if err != nil {
		return r, err
	}
	if len(b) > 0 && json.Unmarshal(b, &r) != nil {
		return r, errors.New("备份记录损坏")
	}
	if r.Repository != "" && r.Repository != a.unified.Fingerprint() {
		r = backupRecord{}
	}
	return r, nil
}
func (a *LibraryAPI) saveUnifiedRecord(r backupRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return a.store.SetSetting("backup-unified", b)
}
func (a *LibraryAPI) unifiedStatus() (any, error) {
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	r, err := a.readUnifiedRecord()
	if err != nil {
		return nil, err
	}
	if a.backupStateErrors["unified"] != nil {
		return nil, a.backupStateErrors["unified"]
	}
	root, err := a.store.RecoveryRecord()
	if err != nil {
		return nil, err
	}
	verified := root != nil && root.State == "verified"
	next := ""
	if a.backupDailyAt != "" && !a.backupActive {
		next = nextBackup(time.Now(), a.backupDailyAt, r).Format(time.RFC3339)
	}
	if r.Status == "running" && !a.backupActive {
		r.Status = "failed"
		r.Error = "上次备份因服务停止而中断，请重新备份"
	}
	zone, _ := time.Now().Zone()
	return map[string]any{"mode": "unified", "configured": verified, "recoveryVerified": verified, "destinations": a.unified.Destinations(), "running": a.backupActive, "busy": a.backupActive, "last": r, "dailyAt": a.backupDailyAt, "nextRunAt": next, "timeZone": zone}, nil
}
func (a *LibraryAPI) startUnifiedBackup() error {
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	if a.backupActive {
		return library.ErrConflict
	}
	if a.ctx.Err() != nil {
		return errors.New("服务正在停止")
	}
	root, err := a.store.RecoveryRecord()
	if err != nil {
		return err
	}
	if root == nil || root.State != "verified" {
		return errors.New("请在 App 中保存并验证恢复 JSON")
	}
	r, err := a.readUnifiedRecord()
	if err != nil {
		return err
	}
	r.Repository = a.unified.Fingerprint()
	r.Status = "running"
	r.Error = ""
	r.AttemptedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.FinishedAt = ""
	if err = a.saveUnifiedRecord(r); err != nil {
		return err
	}
	a.backupActive = true
	a.backupActiveTarget = "unified"
	a.wg.Add(1)
	if a.backupStateErrors == nil {
		a.backupStateErrors = map[string]error{}
	}
	delete(a.backupStateErrors, "unified")
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 24*time.Hour)
		defer cancel()
		result, err := a.unified.Run(ctx, a.store, "")
		r.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err != nil {
			r.Status = "failed"
			r.Error = err.Error()
		} else {
			r.Status = "passed"
			r.Error = ""
			r.Snapshot = result.Snapshot
			r.File = result.File
			r.Bytes = result.Bytes
			r.SavedAt = r.AttemptedAt
			r.VerifiedAt = r.FinishedAt
			r.ReadVerified = true
			r.CleanupError = result.Warning
		}
		a.backupMu.Lock()
		defer a.backupMu.Unlock()
		if e := a.saveUnifiedRecord(r); e != nil {
			a.backupStateErrors["unified"] = errors.New("无法保存备份结果，尚未确认成功；请检查资料盘")
		}
		a.backupActive = false
	}()
	return nil
}
func (a *LibraryAPI) unifiedHistory(ctx context.Context, after string) (any, error) {
	if !a.backupHistoryMu.TryLock() {
		return nil, library.ErrConflict
	}
	defer a.backupHistoryMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	entries, err := a.unified.History(ctx)
	if err != nil {
		return nil, err
	}
	start := 0
	if after != "" {
		found := false
		for i, e := range entries {
			if e.ID == after {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("历史已变化，请刷新")
		}
	}
	end := min(start+50, len(entries))
	next := ""
	if end < len(entries) {
		next = entries[end-1].ID
	}
	return map[string]any{"snapshots": entries[start:end], "next": next, "total": len(entries)}, nil
}
