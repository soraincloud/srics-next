package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
)

type backupPackageRequest struct {
	File            string `json:"file"`
	RecoveryKeyFile string `json:"recoveryKeyFile"`
}
type backupPackageResponse struct {
	File     string `json:"file"`
	Snapshot string `json:"snapshot"`
}

func exportBackupPackage(ctx context.Context, configPath, binary string, c localConfig, l *library.Library, req backupPackageRequest) (backupPackageResponse, error) {
	var response backupPackageResponse
	if c.BackupRepository == "" {
		return response, errors.New("请先用本地备份向导选择备份目录，并保存、验证该位置的恢复 JSON；手动备份不需要 S3")
	}
	if filepath.Ext(req.File) != ".sricsbackup" {
		return response, errors.New("备份包文件名需以 .sricsbackup 结尾")
	}
	if err := outsideDirectories(req.File, c.Data, c.BackupRepository, filepath.Dir(configPath)); err != nil {
		return response, errors.New("请将备份包保存在资料库、备份仓库与配置目录之外")
	}
	if _, err := os.Lstat(req.File); !os.IsNotExist(err) {
		return response, errors.New("备份包文件已存在或不可写，请选择新文件名；不会覆盖旧备份")
	}
	source := recoverySource{Target: "local", Repository: c.BackupRepository, RecoveryKeyFile: req.RecoveryKeyFile}
	if source.RecoveryKeyFile == "" {
		return response, errors.New("请选择此备份位置已验证的恢复 JSON")
	}
	if err := outsideDirectories(req.RecoveryKeyFile, c.Data, c.BackupRepository, filepath.Dir(configPath)); err != nil {
		return response, errors.New("请将恢复 JSON 保存在资料库、备份仓库与配置目录之外，再选择该文件")
	}
	client, err := source.client(ctx, binary)
	if err != nil {
		return response, err
	}
	key, err := readRecoveryKey(req.RecoveryKeyFile)
	if err != nil {
		return response, err
	}
	record, err := recoveryKeyRecordFor(l, key)
	if err != nil {
		return response, err
	}
	if record.Info.State != "verified" {
		return response, errors.New("恢复 JSON 尚未验证，请先完成恢复钥匙设置")
	}
	access, err := recoveryKeyAccess(ctx, l, req.RecoveryKeyFile)
	if err != nil {
		return response, err
	}
	if access != nil {
		err = verifyPrivateContents(ctx, l, access)
		access.Lock()
		if err != nil {
			return response, errors.New("私密原文件校验失败，未导出备份包")
		}
	}
	snapshot, err := checkSetupBackup(ctx, c, l, binary, "local")
	if err != nil {
		return response, err
	}
	snapshots, err := client.LibrarySnapshots(ctx)
	if err != nil {
		return response, err
	}
	supported := false
	for _, entry := range snapshots {
		if entry.ID == snapshot && slices.Contains(entry.RecoveryKeys, key.ID) {
			supported = true
		}
	}
	if !supported {
		return response, errors.New("最新备份未覆盖所选恢复 JSON，未导出")
	}
	manifest := backup.PackageManifest{RepositoryID: key.RepositoryID, Snapshot: snapshot, RecoveryKeyID: key.ID}
	if err = backup.ExportPackage(ctx, c.BackupRepository, req.File, manifest); err != nil {
		return response, err
	}
	response.File, response.Snapshot = req.File, snapshot
	return response, nil
}

func backupPackageManager(ctx context.Context, path, binary string, input io.Reader) error {
	var req backupPackageRequest
	d := json.NewDecoder(io.LimitReader(input, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errors.New("备份包请求无效")
	}
	c, saved, err := loadConfig(path)
	if err != nil {
		return err
	}
	if !saved {
		return errors.New("请先保存资料库配置")
	}
	if serviceLoaded(ctx, path) {
		return errors.New("请先停止服务，再导出完整备份包")
	}
	l, err := library.Open(c.Data)
	if err != nil {
		return errors.New("资料库正在使用或不可读取，请先停止服务")
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	response, err := exportBackupPackage(ctx, path, binary, c, l, req)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}

func openRecoveryPackage(ctx context.Context, configPath, binary string, request recoveryRequest) (recoveryResponse, error) {
	var response recoveryResponse
	if request.Source.RecoveryKeyFile == "" {
		return response, errors.New("请选择与备份包匹配的恢复 JSON")
	}
	key, err := readRecoveryKey(request.Source.RecoveryKeyFile)
	if err != nil {
		return response, err
	}
	c, _, err := loadConfig(configPath)
	if err != nil {
		return response, err
	}
	dest, err := recoveryDirectory(request.Directory, c.Data, c.BackupRepository, filepath.Dir(configPath))
	if err != nil {
		return response, err
	}
	if err = outsideDirectories(request.Source.PackageFile, dest); err != nil {
		return response, errors.New("备份包不能位于导入目录内")
	}
	if err = outsideDirectories(request.Source.RecoveryKeyFile, dest); err != nil {
		return response, errors.New("恢复 JSON 不能位于导入目录内")
	}
	metadata, err := backup.InspectPackage(request.Source.PackageFile)
	if err != nil {
		return response, err
	}
	if metadata.RepositoryID != key.RepositoryID || metadata.RecoveryKeyID != key.ID {
		return response, errors.New("备份包与恢复 JSON 不匹配，未恢复资料")
	}
	// Extraction happens once, to an explicit new workspace, reused by all later
	// recovery steps. Never cache a 100 GB repository in the application bundle.
	manifest, err := backup.ImportPackage(ctx, request.Source.PackageFile, dest)
	if err != nil {
		return response, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dest)
		}
	}()
	if manifest.RepositoryID != key.RepositoryID || manifest.RecoveryKeyID != key.ID {
		return response, errors.New("备份包与恢复 JSON 不匹配，未恢复资料")
	}
	source := recoverySource{Target: "local", Repository: filepath.Join(dest, "repository"), RecoveryKeyFile: request.Source.RecoveryKeyFile}
	client, err := source.client(ctx, binary)
	if err != nil {
		return response, err
	}
	if err = client.Check(ctx); err != nil {
		return response, errors.New("备份包未通过加密仓库完整读取校验，未恢复资料")
	}
	snapshots, err := client.LibrarySnapshots(ctx)
	if err != nil {
		return response, err
	}
	for _, entry := range snapshots {
		if entry.ID == manifest.Snapshot && slices.Contains(entry.RecoveryKeys, key.ID) {
			response.Snapshots = append(response.Snapshots, entry)
		}
	}
	if len(response.Snapshots) != 1 {
		return response, errors.New("备份包中的最新恢复点无法使用此 JSON")
	}
	response.Repository, response.RecoveryKeyID = source.Repository, key.ID
	ok = true
	return response, nil
}
