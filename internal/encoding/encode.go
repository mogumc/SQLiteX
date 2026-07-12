// Package encoding 提供 SQLiteX 的 Key 编解码逻辑。
//
// Key 结构：[TableID(Uvarint)] + [PrimaryKey(Bytes)]
// 使用 Uvarint 前缀编码保证不同 TableID 在字典序上天然隔离，
// 同一 TableID 下PrimaryKey 保持原始字节序，便于 Pebble 前缀扫描。
package encoding

import "encoding/binary"

// EncodeKey 将 TableID 与 PrimaryKey 拼接为存储层物理 Key。
// tableID 必须以 Uvarint 形式编码，避免定长编码造成的前缀冲突。
func EncodeKey(tableID uint64, pk []byte) []byte {
	buf := make([]byte, binary.MaxVarintLen64+len(pk))
	n := binary.PutUvarint(buf, tableID)
	copy(buf[n:], pk)
	return buf[:n+len(pk)]
}

// DecodeKey 从物理 Key 中还原 TableID 与 PrimaryKey。
// 返回的 pk 切片与 raw 共享底层数组，调用方不得修改。
func DecodeKey(raw []byte) (tableID uint64, pk []byte, err error) {
	if len(raw) == 0 {
		return 0, nil, ErrMalformedKey
	}
	id, n := binary.Uvarint(raw)
	if n <= 0 {
		return 0, nil, ErrMalformedKey
	}
	return id, raw[n:], nil
}
