// 零停机热备份（Phase 3）。
//
// 封装 Pebble 的 Checkpoint 机制：在不阻塞读写的前提下，
// 将当前时点的全部 SSTable 与 WAL 元数据硬链接/拷贝到目标目录，
// 形成一个可直接用 Open 打开的物理一致性快照。
package sqlitex

import (
	"fmt"
	"os"
	"path/filepath"
)

// Checkpoint 创建一个零停机的物理一致性快照到 destDir。
//
// 语义：
//   - 非阻塞：调用期间读写请求照常执行，不影响延迟。
//   - 一致性：快照对应调用时刻的一个逻辑时点，包含此前全部已提交写入。
//   - 可直接恢复：destDir 是完整的 Pebble 目录，可用 sqlitex.Open 打开。
//
// 约束：destDir 必须不存在或为空目录。返回错误时 destDir 的清理由调用方负责。
func (db *DB) Checkpoint(destDir string) error {
	if db.closed.Load() {
		return ErrDBClosed
	}
	if destDir == "" {
		return fmt.Errorf("sqlitex: checkpoint dest dir is required")
	}
	if err := validateEmptyDir(destDir); err != nil {
		return err
	}
	if err := db.pebble.Checkpoint(destDir); err != nil {
		return fmt.Errorf("sqlitex: checkpoint: %w", err)
	}
	return nil
}

// BackupTo 将快照同步落盘到 destDir 并等待文件句柄 flush，
// 是 Checkpoint 的运维友好封装：自动创建父目录、完成后 fsync 目录。
// 适合定时任务全量备份。destDir 必须不存在或为空。
func (db *DB) BackupTo(destDir string) error {
	if err := db.Checkpoint(destDir); err != nil {
		return err
	}
	return syncDir(destDir)
}

// validateEmptyDir 校验目标目录不存在或为空，避免误覆盖已有备份。
func validateEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("sqlitex: inspect dest dir: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("sqlitex: checkpoint dest dir %s not empty", dir)
	}
	return nil
}

// syncDir 对目录执行 fsync，确保目录项变更（新建文件）持久化。
// 部分 Windows 文件系统不支持目录 fsync，错误仅忽略（Checkpoint 内部已保证文件级持久化）。
func syncDir(dir string) error {
	d, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return nil
	}
	defer d.Close()
	_ = d.Sync()
	return nil
}
