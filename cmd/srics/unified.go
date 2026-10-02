package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/recoverykey"
	"github.com/soraincloud/srics-next/internal/vault"
)

func unifiedKey(ctx context.Context, action, path string, l *library.Library, c localConfig, req recoveryKeyRequest) (recoveryKeyResponse, error) {
	var out recoveryKeyResponse
	if err := ctx.Err(); err != nil {
		return out, err
	}
	record, err := l.RecoveryRecord()
	if err != nil {
		return out, err
	}
	info := func(r *recoverykey.Record) *recoveryKeyInfo {
		if r == nil {
			return nil
		}
		return &recoveryKeyInfo{ID: r.ID, State: r.State, CreatedAt: r.CreatedAt, VerifiedAt: r.VerifiedAt, VaultPresent: len(r.WrappedVault) > 0}
	}
	if action == "unified-key-status" {
		out.Info = info(record)
		return out, nil
	}
	if filepath.Ext(req.File) != ".json" {
		return out, errors.New("请选择恢复 JSON 文件")
	}
	protected := []string{c.Data, filepath.Dir(path)}
	if c.BackupRepository != "" {
		protected = append(protected, c.BackupRepository)
	}
	if c.Unified != nil {
		protected = append(protected, c.Unified.Repository)
		if c.Unified.Directory != "" {
			protected = append(protected, c.Unified.Directory)
		}
	}
	if err = outsideDirectories(req.File, protected...); err != nil {
		return out, errors.New("请将恢复 JSON 单独保存到资料库与备份位置之外")
	}
	if action == "unified-key-generate" {
		if record != nil && record.State == "verified" {
			return out, errors.New("此资料库已有已验证的恢复 JSON，请继续保管原文件；不会生成第二把钥匙")
		}
		id, e := age.GenerateX25519Identity()
		if e != nil {
			return out, e
		}
		libraryID, e := l.Setting("recovery-library-id")
		if e != nil {
			return out, e
		}
		if len(libraryID) == 0 {
			libraryID = []byte(library.NewID())
		}
		recipient := id.Recipient().String()
		now := time.Now().UTC()
		record = &recoverykey.Record{ID: recoverykey.Fingerprint(recipient), LibraryID: string(libraryID), Recipient: recipient, CreatedAt: now, State: "exported", ExportPath: req.File}
		wrapped, e := l.Setting("vault-key")
		if e != nil {
			return out, e
		}
		if len(wrapped) > 0 {
			identity, e := vault.Unlock(wrapped, req.VaultPassword)
			if e != nil {
				return out, &fieldError{Field: "vaultPassword", Message: "当前保险库口令不正确，未生成恢复 JSON"}
			}
			plain := []byte(identity.String())
			defer clear(plain)
			record.WrappedVault, err = recoverykey.Seal(recipient, plain)
			record.WrappedVaultHash = recoverykey.Fingerprint(string(record.WrappedVault))
			if err != nil {
				return out, err
			}
		}
		key := recoveryKeyFile{Format: "srics-recovery-key", Version: 2, ID: record.ID, LibraryID: record.LibraryID, Recipient: recipient, CreatedAt: now, Secret: id.String(), Instructions: "此 JSON 是资料库的离线应急恢复钥匙。保管这份文件和一份完整 .sricsbackup 备份，即可在原机器、配置、登录密码与保险库口令丢失时恢复普通及私密原文件。程序只保存公开 recipient；此文件不可放入资料库或备份中。首次需在 App 重新选择验证。此后启用私密区、修改密码、更换保存位置无需更换 JSON。旧版备份仍需其旧 JSON。"}
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if err = atomicfile.WriteNew(req.File, func(w io.Writer) error { e := json.NewEncoder(w); e.SetIndent("", "  "); return e.Encode(key) }); err != nil {
			return out, errors.New("恢复 JSON 未保存，请选择一个可写的新文件名")
		}
		data, e := json.Marshal(record)
		if e != nil {
			return out, e
		}
		if err = l.SetSettings(map[string][]byte{"recovery-library-id": libraryID, recoverykey.Setting: data}); err != nil {
			return out, errors.New("JSON 已导出，但资料库记录未保存；请保留文件并重新检查设置")
		}
		out.Info, out.File = info(record), req.File
		return out, nil
	}
	if action != "unified-key-confirm" {
		return out, errors.New("未知恢复钥匙操作")
	}
	key, err := readRecoveryKey(req.File)
	if err != nil {
		return out, err
	}
	if key.Version != 2 || record == nil || key.ID != record.ID || key.LibraryID != record.LibraryID {
		return out, errors.New("请选择此资料库刚保存的恢复 JSON")
	}
	access, err := recoveryKeyAccess(ctx, l, req.File)
	if err != nil {
		return out, err
	}
	if access != nil {
		err = verifyPrivateContents(ctx, l, access)
		access.Lock()
		if err != nil {
			return out, errors.New("恢复 JSON 未通过私密原件完整校验")
		}
	}
	now := time.Now().UTC()
	record.State = "verified"
	record.ExportPath = req.File
	record.VerifiedAt = &now
	data, err := json.Marshal(record)
	if err != nil {
		return out, err
	}
	if err = l.SetSetting(recoverykey.Setting, data); err != nil {
		return out, err
	}
	out.Info = info(record)
	return out, nil
}

type unifiedSetupRequest struct {
	Directory   string             `json:"directory"`
	Cloud       *backup.S3Config   `json:"cloud,omitempty"`
	Credentials backup.Credentials `json:"credentials"`
	File        string             `json:"file"`
}
type unifiedResponse struct {
	Config localConfig           `json:"config"`
	Result *backup.PackageResult `json:"result,omitempty"`
}

func unifiedManager(ctx context.Context, action, path, binary string, input io.Reader) error {
	c, saved, err := loadConfig(path)
	if err != nil {
		return err
	}
	if !saved {
		return errors.New("请先完成资料库设置")
	}
	if serviceLoaded(ctx, path) {
		return errors.New("请先停止服务，再修改本机备份设置或保存备份文件")
	}
	l, err := library.Open(c.Data)
	if err != nil {
		return errors.New("资料库正在使用，请先停止服务")
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	decoder := json.NewDecoder(io.LimitReader(input, 65537))
	decoder.DisallowUnknownFields()
	if action == "unified-key-status" || action == "unified-key-generate" || action == "unified-key-confirm" {
		var req recoveryKeyRequest
		if decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("恢复钥匙请求无效")
		}
		out, e := unifiedKey(ctx, action, path, l, c, req)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	var req unifiedSetupRequest
	if decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("备份设置请求无效")
	}
	if action == "unified-backup-configure" {
		root, e := l.RecoveryRecord()
		if e != nil {
			return e
		}
		if root == nil || root.State != "verified" {
			return errors.New("请先保存并验证恢复 JSON")
		}
		candidate := backup.UnifiedConfig{Directory: req.Directory, Cloud: req.Cloud}
		// Reuse the managed cache when its keys are readable. Never change old repos.
		if c.Unified != nil {
			candidate.Repository = c.Unified.Repository
			candidate.PasswordFile = c.Unified.PasswordFile
		}
		// Saving a destination explicitly can replace an unreadable regenerable
		// cache. Daily backup never recreates it. Old cache directories survive.
		if c.Unified != nil {
			previous := backup.Unified{Config: *c.Unified, Binary: binary, ProtectedDirectory: filepath.Dir(path)}
			client, e := previous.Client()
			if e == nil {
				_, e = client.RepositoryID(ctx)
			}
			if e != nil {
				candidate.Repository = ""
				candidate.PasswordFile = ""
			}
		}
		created := candidate.Repository == ""
		if created {
			dir := filepath.Join(filepath.Dir(path), "backup-cache", library.NewID())
			candidate.Repository = filepath.Join(dir, "repository")
			candidate.PasswordFile = filepath.Join(dir, "daily-key")
		}
		if candidate.Cloud != nil {
			candidate.CredentialsFile = filepath.Join(filepath.Dir(path), "backup-access", library.NewID()+".credentials")
		}
		if err = candidate.Validate(c.Data); err != nil {
			return err
		}
		if candidate.Directory != "" {
			if err = outsideDirectories(candidate.Directory, filepath.Dir(path)); err != nil {
				return errors.New("请选择配置目录之外的备份位置")
			}
			info, e := os.Stat(candidate.Directory)
			if e != nil || !info.IsDir() {
				return errors.New("备份位置不存在，请选择已挂载的目录")
			}
		}
		if candidate.Cloud != nil {
			if req.Credentials.AccessKeyID == "" && req.Credentials.SecretAccessKey == "" && c.Unified != nil && c.Unified.Cloud != nil {
				oldClient, e := (backup.Unified{Config: *c.Unified}).CloudClient()
				if e != nil {
					return e
				}
				req.Credentials = oldClient.Credentials
			}
			if err = req.Credentials.Validate(); err != nil {
				return err
			}
			probe := backup.Client{S3: candidate.Cloud, Credentials: req.Credentials}
			if err = probe.ProbePackages(ctx); err != nil {
				return err
			}
		}
		if created {
			if err = os.MkdirAll(filepath.Dir(candidate.Repository), 0700); err != nil {
				return err
			}
			secret := make([]byte, 32)
			if _, err = rand.Read(secret); err != nil {
				return err
			}
			defer clear(secret)
			password := base64.RawURLEncoding.EncodeToString(secret)
			if err = atomicfile.WriteNew(candidate.PasswordFile, func(w io.Writer) error { _, e := io.WriteString(w, password); return e }); err != nil {
				return err
			}
			if err = (backup.Client{Binary: binary, Repository: candidate.Repository, Password: password}).Init(ctx); err != nil {
				return err
			}
		}
		if candidate.Cloud != nil {
			if err = os.MkdirAll(filepath.Dir(candidate.CredentialsFile), 0700); err != nil {
				return err
			}
			if err = atomicfile.WriteNew(candidate.CredentialsFile, func(w io.Writer) error { return json.NewEncoder(w).Encode(req.Credentials) }); err != nil {
				return err
			}
		}
		plan := backup.Unified{Config: candidate, Binary: binary, ProtectedDirectory: filepath.Dir(path)}
		if _, err = plan.Client(); err != nil {
			return err
		}
		// Configuration stays saved on a failed first backup, so retry never rotates
		// the cache key or the user's already verified emergency JSON.
		c.Unified = &candidate
		if err = writeConfig(path, c); err != nil {
			return err
		}
	}
	if c.Unified == nil {
		return errors.New("请先完成新的统一备份设置")
	}
	plan := backup.Unified{Config: *c.Unified, Binary: binary, ProtectedDirectory: filepath.Dir(path)}
	result, err := plan.Run(ctx, l, req.File)
	if e := saveUnifiedResult(l, plan, result, err); e != nil {
		return e
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(unifiedResponse{Config: c, Result: &result})
}
func saveUnifiedResult(l *library.Library, plan backup.Unified, r backup.PackageResult, runErr error) error {
	// The server reads this same status. Failure preserves the previous success.
	b, e := l.Setting("backup-unified")
	if e != nil {
		return e
	}
	record := map[string]any{}
	if len(b) > 0 && json.Unmarshal(b, &record) != nil {
		return errors.New("备份结果记录损坏")
	}
	if old, ok := record["repository"].(string); ok && old != plan.Fingerprint() {
		record = map[string]any{}
	}
	now := time.Now().UTC()
	record["repository"] = plan.Fingerprint()
	record["finishedAt"] = now
	record["attemptedAt"] = now
	if runErr != nil {
		record["status"] = "failed"
		record["error"] = runErr.Error()
	} else {
		record["status"] = "passed"
		record["error"] = ""
		record["savedAt"] = now
		record["verifiedAt"] = now
		record["readVerified"] = true
		record["snapshot"] = r.Snapshot
		record["file"] = r.File
		record["bytes"] = r.Bytes
		record["cleanupError"] = r.Warning
	}
	data, e := json.Marshal(record)
	if e != nil {
		return e
	}
	return l.SetSetting("backup-unified", data)
}
