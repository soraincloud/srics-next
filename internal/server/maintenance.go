package server

import (
	"context"
	"errors"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"time"
)

func (s *Server) StartMaintenance(days int, p backup.Retention) {
	a := s.library
	a.trashDays = days
	a.retention = p
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			err := a.store.ExpireTrash(a.ctx, days)
			if err == nil {
				err = a.store.Collect()
			}
			a.maintenanceMu.Lock()
			a.maintenanceError = ""
			if err != nil {
				a.maintenanceError = "资料清理未完成，请检查资料盘；将在下次重试"
			}
			a.maintenanceMu.Unlock()
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (a *LibraryAPI) retentionPreview(ctx context.Context, target string) (backup.RetentionPlan, error) {
	if a.unified != nil {
		return backup.RetentionPlan{}, errors.New("统一备份保留全部文件，请在保存位置整理日期版本")
	}
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	if a.backupActive {
		return backup.RetentionPlan{}, library.ErrConflict
	}
	if a.backupClient(target).Repository == "" {
		return backup.RetentionPlan{}, errors.New("备份目标未配置")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return a.backupClient(target).PlanRetention(ctx, a.retention)
}
func (a *LibraryAPI) cleanBackup(target, token string) error {
	if a.unified != nil {
		return errors.New("统一备份保留全部日期文件")
	}
	a.backupMu.Lock()
	defer a.backupMu.Unlock()
	if a.backupActive {
		return library.ErrConflict
	}
	if a.ctx.Err() != nil {
		return a.ctx.Err()
	}
	record, err := a.readBackupRecord(target)
	if err != nil {
		return err
	}
	if !record.ReadVerified || record.Snapshot == "" {
		return errors.New("请先完成一次备份和完整读取检查")
	}
	if !a.retention.Enabled {
		return errors.New("请先在本机程序中启用保留策略")
	}
	a.backupActive = true
	a.backupActiveTarget = target
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 24*time.Hour)
		defer cancel()
		client := a.backupClient(target)
		err := client.Check(ctx)
		if err == nil {
			err = client.ApplyRetention(ctx, a.retention, token)
		}
		record.CleanupError = ""
		if err != nil {
			record.CleanupError = "备份清理未完成，请检查目标或重新预览；已完成的删除不会回滚"
		} else {
			record.CleanupAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		a.backupHistoryMu.Lock()
		delete(a.backupHistories, target)
		a.backupHistoryMu.Unlock()
		a.backupMu.Lock()
		defer a.backupMu.Unlock()
		if e := a.saveBackupRecord(record, target); e != nil {
			if a.backupStateErrors == nil {
				a.backupStateErrors = map[string]error{}
			}
			a.backupStateErrors[target] = e
		}
		a.backupActive = false
	}()
	return nil
}
