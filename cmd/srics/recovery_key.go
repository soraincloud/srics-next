package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"filippo.io/age"
	"fmt"
	"github.com/soraincloud/srics-next/internal/recoverykey"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

// Only this exported file contains the recovery secret. The library stores a
// password-wrapped vault identity and public status, never the secret itself.
type recoveryKeyFile struct {
	Format       string    `json:"format"`
	Version      int       `json:"version"`
	ID           string    `json:"id"`
	Recipient    string    `json:"recipient,omitempty"`
	RepositoryID string    `json:"repositoryID"`
	LibraryID    string    `json:"libraryID"`
	CreatedAt    time.Time `json:"createdAt"`
	Secret       string    `json:"secret"`
	Instructions string    `json:"instructions"`
}
type recoveryKeyInfo struct {
	ID           string     `json:"id"`
	RepositoryID string     `json:"repositoryID"`
	State        string     `json:"state"`
	CreatedAt    time.Time  `json:"createdAt"`
	VerifiedAt   *time.Time `json:"verifiedAt,omitempty"`
	Snapshot     string     `json:"snapshot,omitempty"`
	VaultPresent bool       `json:"vaultPresent"`
}
type recoveryKeyRecord struct {
	LocationDigest string          `json:"locationDigest"`
	Info           recoveryKeyInfo `json:"info"`
	WrappedVault   []byte          `json:"wrappedVault,omitempty"`
	VaultDigest    string          `json:"vaultDigest"` // Detect a changed vault between export and confirmation.
}
type recoveryKeyRequest struct {
	Target        string `json:"target"`
	File          string `json:"file"`
	VaultPassword string `json:"vaultPassword"`
}
type recoveryKeyResponse struct {
	Info *recoveryKeyInfo `json:"info,omitempty"`
	File string           `json:"file,omitempty"`
}

func digest(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func validDigest(s string) bool { b, err := hex.DecodeString(s); return err == nil && len(b) == 32 }
func readRecoveryKey(path string) (*recoveryKeyFile, error) {
	// Offline copies on USB or synced folders may not preserve POSIX modes.
	// Export still uses 0600; v1 keeps its original strict permission rule.
	if !filepath.IsAbs(path) {
		return nil, errors.New("请选择恢复 JSON 的绝对路径")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("无法读取恢复 JSON")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("恢复 JSON 需为普通文件")
	}
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if len(data) > 16384 {
		clear(data)
		return nil, errors.New("恢复 JSON 文件过大")
	}
	if err != nil {
		return nil, errors.New("无法读取恢复文件；请选择仅本人可读的文件（权限 600）")
	}
	defer clear(data)
	var key recoveryKeyFile
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&key) != nil || d.Decode(&struct{}{}) != io.EOF || key.Format != "srics-recovery-key" || (key.Version != 1 && key.Version != 2) || key.CreatedAt.IsZero() || (key.Version == 1 && !validDigest(key.RepositoryID)) || !library.IDPattern.MatchString(key.LibraryID) {
		return nil, errors.New("恢复文件格式无效或版本不支持")
	}
	if key.Version == 2 {
		id, e := age.ParseX25519Identity(key.Secret)
		if e != nil || key.RepositoryID != "" || id.Recipient().String() != key.Recipient || key.ID != recoverykey.Fingerprint(key.Recipient) {
			return nil, errors.New("恢复密钥校验失败")
		}
		return &key, nil
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("旧版恢复文件需仅本人可读（权限 600）")
	}
	secret, err := base64.RawURLEncoding.DecodeString(key.Secret)
	defer clear(secret)
	if err != nil || len(secret) != 32 || key.ID != digest([]byte(key.Secret)) {
		return nil, errors.New("恢复密钥校验失败，文件可能已损坏")
	}
	return &key, nil
}
func recoveryKeyRecordFor(l *library.Library, key *recoveryKeyFile) (recoveryKeyRecord, error) {
	var record recoveryKeyRecord
	id, err := l.Setting("recovery-library-id")
	if err != nil || string(id) != key.LibraryID {
		return record, errors.New("该恢复点不支持此恢复密钥，或密钥属于其他资料库")
	}
	data, err := l.Setting("recovery-key-" + key.ID)
	if err != nil || len(data) == 0 || json.Unmarshal(data, &record) != nil || record.Info.ID != key.ID || record.Info.RepositoryID != key.RepositoryID {
		return record, errors.New("该恢复点没有对应的恢复密钥记录，请选择启用密钥后创建的备份")
	}
	wrapped, err := l.Setting("vault-key")
	if err != nil {
		return record, err
	}
	if (len(wrapped) > 0) != record.Info.VaultPresent {
		return record, errors.New("此密钥未覆盖当前私密区，请重新生成恢复密钥并备份")
	}
	return record, nil
}

func (s recoverySource) recoveryKeyClient(ctx context.Context, binary string) (backup.Client, error) {
	var client backup.Client
	key, err := readRecoveryKey(s.RecoveryKeyFile)
	if err != nil {
		return client, err
	}
	client.Binary, client.Password = binary, key.Secret
	repositoryID := key.RepositoryID
	if key.Version == 2 {
		envelope, e := recoverykey.OpenEnvelope(key.Secret, s.RecoveryEnvelope, key.LibraryID)
		if e != nil {
			return client, e
		}
		client.Password = envelope.Password
		repositoryID = envelope.RepositoryID
	}
	if s.Target == "local" {
		if !filepath.IsAbs(s.Repository) {
			return client, errors.New("请选择备份目录的绝对路径")
		}
		if err = outsideDirectories(s.RecoveryKeyFile, s.Repository); err != nil {
			return client, err
		}
		client.Repository = s.Repository
	} else if s.Target == "cloud" {
		if err = s.Cloud.Connection.Validate(); err != nil {
			return client, err
		}
		data, e := privateFile(s.Cloud.CredentialsFile, 32768)
		if e != nil {
			return client, e
		}
		defer clear(data)
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&client.Credentials) != nil || d.Decode(&struct{}{}) != io.EOF {
			return client, errors.New("云端凭据格式无效")
		}
		if err = client.Credentials.Validate(); err != nil {
			return client, err
		}
		connection := s.Cloud.Connection
		client.S3, client.Repository = &connection, connection.Repository()
	} else {
		return client, errors.New("请选择本地备份或云端 S3")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	id, err := client.RepositoryID(ctx)
	if err != nil || id != repositoryID {
		return client, errors.New("恢复密钥无法解锁此仓库，请确认已启用、仓库完整且连接正确")
	}
	return client, nil
}

func generateRecoveryKey(ctx context.Context, c localConfig, configPath string, client backup.Client, l *library.Library, req recoveryKeyRequest) (recoveryKeyResponse, error) {
	var out recoveryKeyResponse
	if err := outsideDirectories(req.File, c.Data, c.BackupRepository, filepath.Dir(configPath)); err != nil {
		return out, errors.New("请将恢复文件保存在资料库、备份仓库和程序配置目录之外")
	}
	repositoryID, err := client.RepositoryID(ctx)
	if err != nil {
		return out, err
	}
	wrapped, err := l.Setting("vault-key")
	if err != nil {
		return out, err
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return out, err
	}
	defer clear(secret)
	key := recoveryKeyFile{Format: "srics-recovery-key", Version: 1, RepositoryID: repositoryID, CreatedAt: time.Now().UTC(), Secret: base64.RawURLEncoding.EncodeToString(secret)}
	key.ID = digest([]byte(key.Secret))
	key.Instructions = "此文件是备用总钥匙，请离线保管。App → 从备份恢复 → 恢复密钥。先在生成密钥的 App 中重新选择此文件验证并备份，成功后方可使用。仅覆盖记录了此密钥的资料库恢复点；新仓库需重新启用。云端登录凭据需另存。应急手动恢复：secret 可作为 restic 仓库密码；index.db/settings 中 recovery-key-加本文件 id 的 wrappedVault 是 base64 编码的 age 密文，用 secret 解密可得到私密文件的 age X25519 身份。"
	libraryID, err := l.Setting("recovery-library-id")
	if err != nil {
		return out, err
	}
	if len(libraryID) == 0 {
		libraryID = []byte(library.NewID())
	}
	key.LibraryID = string(libraryID)
	record := recoveryKeyRecord{LocationDigest: digest([]byte(client.Repository)), Info: recoveryKeyInfo{ID: key.ID, RepositoryID: repositoryID, State: "exported", CreatedAt: key.CreatedAt, VaultPresent: len(wrapped) > 0}, VaultDigest: digest(wrapped)}
	if len(wrapped) > 0 {
		identity, e := vault.Unlock(wrapped, req.VaultPassword)
		if e != nil {
			return out, errors.New("当前私密区密码不正确，未生成恢复文件")
		}
		record.WrappedVault, err = vault.Wrap(identity, key.Secret)
		if err != nil {
			return out, err
		}
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = atomicfile.WriteNew(req.File, func(w io.Writer) error { e := json.NewEncoder(w); e.SetIndent("", "  "); return e.Encode(key) }); err != nil {
		return out, errors.New("无法导出恢复文件，请选择可写的新文件位置；不会覆盖已有文件")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return out, err
	}
	if err = l.SetSettings(map[string][]byte{"recovery-library-id": libraryID, "recovery-key-" + key.ID: data, "recovery-key-active-" + req.Target: []byte(key.ID)}); err != nil {
		return out, errors.New("恢复文件已导出，但记录未保存，尚未启用；请重新生成")
	}
	out.Info, out.File = &record.Info, req.File
	return out, nil
}

func confirmRecoveryKey(ctx context.Context, c localConfig, configPath string, client backup.Client, l *library.Library, req recoveryKeyRequest) (recoveryKeyResponse, error) {
	var out recoveryKeyResponse
	if err := outsideDirectories(req.File, c.Data, c.BackupRepository, filepath.Dir(configPath)); err != nil {
		return out, err
	}
	key, err := readRecoveryKey(req.File)
	if err != nil {
		return out, err
	}
	record, err := recoveryKeyRecordFor(l, key)
	if err != nil {
		return out, err
	}
	wrapped, err := l.Setting("vault-key")
	if err != nil {
		return out, err
	}
	// For an already verified key a password rewrap does not invalidate the key.
	if record.Info.State != "verified" && digest(wrapped) != record.VaultDigest {
		return out, errors.New("导出后私密区配置已变化，请重新生成恢复密钥")
	}
	if record.Info.VaultPresent {
		identity, e := vault.Unlock(record.WrappedVault, key.Secret)
		if e != nil {
			return out, errors.New("恢复文件无法解锁私密区，未启用")
		}
		a := vault.NewAccess(ctx, identity, 24*time.Hour)
		e = verifyPrivateContents(ctx, l, a)
		a.Lock()
		if e != nil {
			return out, errors.New("恢复密钥未通过私密索引与原件校验")
		}
	}
	if err = client.AddRecoveryPassword(ctx, key.Secret, key.RepositoryID); err != nil {
		return out, err
	}
	recovery := client
	recovery.Password = key.Secret
	stage := filepath.Join(l.Root, "staging", "recovery-key-"+library.NewID())
	defer os.RemoveAll(stage)
	if err = l.Snapshot(ctx, stage); err != nil {
		return out, err
	}
	pinned, err := library.Open(stage)
	if err != nil {
		return out, err
	}
	err = pinned.Verify(ctx)
	closeErr := pinned.Close()
	if err != nil {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	id, err := recovery.BackupLibrary(ctx, stage)
	if err != nil {
		return out, errors.New("恢复密钥已登记，但新备份未完成，请使用同一恢复文件重试")
	}
	if err = recovery.Check(ctx); err != nil {
		return out, errors.New("备份未通过完整读取校验，尚未确认启用，请使用同一恢复文件重试")
	}
	verified := time.Now().UTC()
	record.Info.State, record.Info.Snapshot, record.Info.VerifiedAt = "verified", id, &verified
	record.LocationDigest = digest([]byte(client.Repository))
	data, err := json.Marshal(record)
	if err != nil {
		return out, err
	}
	backupSetting := "backup"
	if req.Target == "cloud" {
		backupSetting = "backup-cloud"
	}
	backupData, err := json.Marshal(map[string]any{"repository": digest([]byte(client.Repository)), "initialized": true, "status": "passed", "attemptedAt": verified, "finishedAt": verified, "savedAt": verified, "verifiedAt": verified, "snapshot": id, "readVerified": true})
	if err != nil {
		return out, err
	}
	if err = l.SetSettings(map[string][]byte{"recovery-key-" + key.ID: data, "recovery-key-active-" + req.Target: []byte(key.ID), backupSetting: backupData}); err != nil {
		return out, errors.New("备份已完成，但验证记录未保存，请使用同一恢复文件重试")
	}
	out.Info = &record.Info
	return out, nil
}

func recoveryKeyManager(ctx context.Context, action, path, binary string, input io.Reader) error {
	var req recoveryKeyRequest
	d := json.NewDecoder(io.LimitReader(input, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(&struct{}{}) != io.EOF || (req.Target != "local" && req.Target != "cloud") {
		return errors.New("恢复密钥请求无效")
	}
	c, _, err := loadConfig(path)
	if err != nil {
		return err
	}
	l, err := library.Open(c.Data)
	if err != nil {
		return errors.New("请先停止服务，再管理恢复密钥")
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	var out recoveryKeyResponse
	if action == "recovery-key-status" {
		id, e := l.Setting("recovery-key-active-" + req.Target)
		if e != nil {
			return e
		}
		if len(id) > 0 {
			data, e := l.Setting("recovery-key-" + string(id))
			if e != nil {
				return e
			}
			var record recoveryKeyRecord
			if json.Unmarshal(data, &record) != nil {
				return errors.New("恢复密钥状态无法读取")
			}
			wrapped, e := l.Setting("vault-key")
			if e != nil {
				return e
			}
			if (len(wrapped) > 0) != record.Info.VaultPresent {
				record.Info.State = "outdated"
			}
			repository := c.BackupRepository
			if req.Target == "cloud" {
				repository = c.Cloud.Connection.Repository()
			}
			if digest([]byte(repository)) != record.LocationDigest {
				record.Info.State = "outdated"
			}
			out.Info = &record.Info
		}
	} else {
		var client backup.Client
		if req.Target == "cloud" {
			client, err = configuredCloud(c, binary)
		} else {
			client, err = configuredBackup(c, binary)
		}
		if err != nil {
			return err
		}
		if client.Repository == "" {
			return errors.New("请先保存此备份目标的配置并完成一次备份")
		}
		switch action {
		case "recovery-key-generate":
			out, err = generateRecoveryKey(ctx, c, path, client, l, req)
		case "recovery-key-confirm":
			out, err = confirmRecoveryKey(ctx, c, path, client, l, req)
		default:
			return errors.New("未知恢复密钥操作")
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

// Resolves the private identity from the backup itself, with no original machine,
// login password, vault password or application configuration required.
func recoveryKeyAccess(ctx context.Context, l *library.Library, file string) (*vault.Access, error) {
	key, err := readRecoveryKey(file)
	if err != nil {
		return nil, err
	}
	if key.Version == 2 {
		record, e := l.RecoveryRecord()
		if e != nil {
			return nil, e
		}
		if record == nil || record.ID != key.ID || record.LibraryID != key.LibraryID {
			return nil, errors.New("恢复 JSON 与资料库不匹配")
		}
		wrapped, e := l.Setting("vault-key")
		if e != nil {
			return nil, e
		}
		if (len(wrapped) > 0) != (len(record.WrappedVault) > 0) {
			return nil, errors.New("恢复密钥未覆盖私密资料")
		}
		if len(wrapped) == 0 {
			return nil, nil
		}
		plain, e := recoverykey.Open(key.Secret, record.WrappedVault)
		if e != nil {
			return nil, errors.New("恢复 JSON 无法解锁私密资料")
		}
		defer clear(plain)
		id, e := age.ParseX25519Identity(string(plain))
		if e != nil {
			return nil, e
		}
		return vault.NewAccess(ctx, id, 24*time.Hour), nil
	}
	record, err := recoveryKeyRecordFor(l, key)
	if err != nil {
		return nil, err
	}
	if !record.Info.VaultPresent {
		return nil, nil
	}
	identity, err := vault.Unlock(record.WrappedVault, key.Secret)
	if err != nil {
		return nil, errors.New("恢复密钥无法解锁此恢复点的私密区")
	}
	a := vault.NewAccess(ctx, identity, 24*time.Hour)
	if _, err = l.PrivateItems(ctx, a); err != nil {
		a.Lock()
		return nil, errors.New("恢复密钥未通过私密索引校验")
	}
	return a, nil
}

// PrivateRead authenticates the entire plaintext, its size and hash before
// returning a reader. Closing it immediately verifies without exporting any
// plaintext or reading the file twice. Include trash and private previews too.
func verifyPrivateContents(ctx context.Context, l *library.Library, a *vault.Access) error {
	items, err := l.PrivateItems(ctx, a)
	if err != nil {
		return err
	}
	for _, it := range items {
		for _, thumb := range []bool{false, true} {
			if thumb && it.Thumb == "" {
				continue
			}
			r, _, err := l.PrivateRead(ctx, a, it, thumb)
			if err != nil {
				return err
			}
			if err = r.Close(); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func resetRecoveryPasswords(ctx context.Context, path string, req recoveryRequest) (recoveryResponse, error) {
	var out recoveryResponse
	if strings.ContainsAny(req.NewPassword, "\r\n") || strings.ContainsAny(req.NewVaultPassword, "\r\n") {
		return out, errors.New("新登录密码和新保险库口令不能包含换行符，请删除后重新设置")
	}
	if len(req.NewPassword) < 12 || len(req.NewPassword) > 72 {
		return out, errors.New("新登录密码需为 12–72 字节")
	}
	c, _, err := loadConfig(path)
	if err != nil {
		return out, err
	}
	dest, err := recoveryDirectory(req.Directory, c.Data, c.BackupRepository, filepath.Dir(path))
	if err != nil {
		return out, err
	}
	_, l, err := checkedRecovery(ctx, dest)
	if err != nil {
		return out, err
	}
	defer l.Close()
	a, err := recoveryKeyAccess(ctx, l, req.Source.RecoveryKeyFile)
	if err != nil {
		return out, err
	}
	values := map[string][]byte{}
	if a != nil {
		defer a.Lock()
		if len(req.NewVaultPassword) < 12 || len(req.NewVaultPassword) > 1024 {
			return out, errors.New("新私密区密码需为 12–1024 字节")
		}
		key, _, _, e := a.Snapshot()
		if e != nil {
			return out, e
		}
		values["vault-key"], err = vault.Wrap(key, req.NewVaultPassword)
		if err != nil {
			return out, err
		}
	}
	values["password"], err = bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		return out, err
	}
	if err = l.SetSettings(values); err != nil {
		return out, err
	}
	out.PasswordsReset = true
	return out, nil
}
