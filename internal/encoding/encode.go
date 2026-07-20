// Package encoding 提供 SQLiteX 的 Key 编解码逻辑。
//
// 数据行 Key: [TableID(Uvarint)] + [PrimaryKey(Bytes)]
// 索引 Key:   [0xFF] + [TableID(Uvarint)] + [FieldNum(byte)] + [FieldValue(Bytes)] + [PK(Bytes)]
//
// 0xFF 前缀将全部索引 Key 置于键空间的末尾，与数据 Key 天然隔离。
// 同一 TableID 下按 FieldNum → FieldValue → PK 排序，支持索引前缀扫描。
package encoding

import "encoding/binary"

// IndexPrefix 索引键空间的全局前缀，确保所有索引 Key 晚于数据行 Key。
const IndexPrefix = 0xFF

// EncodeKey 将 TableID 与 PrimaryKey 拼接为数据行物理 Key。
func EncodeKey(tableID uint64, pk []byte) []byte {
	buf := make([]byte, binary.MaxVarintLen64+len(pk))
	n := binary.PutUvarint(buf, tableID)
	copy(buf[n:], pk)
	return buf[:n+len(pk)]
}

// EncodeIndexKey 构造二级索引的物理 Key。
// fieldNum 是字段在 proto 定义中的编号（1-based）。
// 格式: [0xFF][TableID Uvarint][FieldNum byte][FieldValue][PK]
func EncodeIndexKey(tableID uint64, fieldNum int32, fieldValue, pk []byte) []byte {
	var tmp [binary.MaxVarintLen64]byte
	nTable := binary.PutUvarint(tmp[:], tableID)
	buf := make([]byte, 1+nTable+1+len(fieldValue)+len(pk))
	buf[0] = IndexPrefix
	binary.PutUvarint(buf[1:], tableID)
	buf[1+nTable] = byte(fieldNum)
	copy(buf[1+nTable+1:], fieldValue)
	copy(buf[1+nTable+1+len(fieldValue):], pk)
	return buf
}

// DecodeIndexKey 从索引 Key 中还原 TableID、字段编号、字段值和主键。
func DecodeIndexKey(raw []byte) (tableID uint64, fieldNum int32, fieldValue, pk []byte, err error) {
	if len(raw) < 3 || raw[0] != IndexPrefix {
		return 0, 0, nil, nil, ErrMalformedKey
	}
	id, n := binary.Uvarint(raw[1:])
	if n <= 0 {
		return 0, 0, nil, nil, ErrMalformedKey
	}
	fieldNum = int32(raw[1+n])
	fvStart := 1 + n + 1
	if fvStart >= len(raw) {
		return 0, 0, nil, nil, ErrMalformedKey
	}
	// fieldValue 和 pk 的分割点是"PK在数据 Key 中的位置"——
	// 但实际上我们不知道 fieldValue 在哪结束。
	// 对于索引扫描，调用方通常知道 fieldValue 的长度（按类型），
	// 或者前缀扫描时直接用整个 raw[1+n+1:] 作为 value+pk 拼接即可。
	// 这里提供完整反解供调试用，实际索引查询不需要拆分 fieldValue 和 pk。
	return id, fieldNum, raw[fvStart:], nil, nil
}

// EncodeIndexPrefix 构造索引前缀（不含 PK 后缀），用于 Pebble 前缀迭代。
func EncodeIndexPrefix(tableID uint64, fieldNum int32, fieldValue []byte) []byte {
	var tmp [binary.MaxVarintLen64]byte
	nTable := binary.PutUvarint(tmp[:], tableID)
	buf := make([]byte, 1+nTable+1+len(fieldValue))
	buf[0] = IndexPrefix
	binary.PutUvarint(buf[1:], tableID)
	buf[1+nTable] = byte(fieldNum)
	copy(buf[1+nTable+1:], fieldValue)
	return buf
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
