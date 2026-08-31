// Package sqlitex 提供嵌入式、编译时优化的键值存储能力。
//
// 核心设计：以 Protobuf 作为 Schema，编译时通过代码生成消除运行时反射，
// 底层使用 Pebble 引擎驱动，MPSC 队列实现高吞吐写入。
package sqlitex

import "errors"

// 哨兵错误：调用方通过 errors.Is 匹配，禁止字符串比较。
var (
	// ErrDBClosed 数据库已关闭，所有操作立即返回此错误。
	ErrDBClosed = errors.New("sqlitex: database is closed")

	// ErrInvalidKey 写入的 Key 为空。
	ErrInvalidKey = errors.New("sqlitex: key must not be empty")

	// ErrWriteThrottled 写队列满或内存超限，触发背压拒绝。
	ErrWriteThrottled = errors.New("sqlitex: write throttled, queue full or memory exceeded")

	// ErrDuplicateKey 违反唯一索引约束：Create/Update 的字段值与既有记录冲突。
	// 通过 errors.Is 匹配；写入未发生（检查在 WriteBatch 提交前）。
	ErrDuplicateKey = errors.New("sqlitex: duplicate key in unique index")
)
