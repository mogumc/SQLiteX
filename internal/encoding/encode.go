// Package encoding 提供 SQLiteX 的 Key 编解码逻辑。
//
// 数据行 Key: [0x00][TableHash 8B 定宽][PrimaryKey(Bytes)]
// 索引 Key:   [0xFF][TableHash 8B 定宽][FieldNum(byte)][ValueLen(Uvarint)][FieldValue(Bytes)][PK(Bytes)]
// 唯一索引:   [0xFF][TableHash 8B 定宽][FieldNum(byte)][ValueLen(Uvarint)][FieldValue(Bytes)]
//
// 0x00 vs 0xFF 两个 tag 字节彻底隔离数据行与索引行的命名空间。
// TableHash 为 8 字节定宽（消息全名的 FNV-1a 64 位哈希），确保 Schema 演进时 TableID 稳定。
// 同一 TableHash 下按 FieldNum → ValueLen → FieldValue → PK 排序，支持索引前缀扫描。
// ValueLen 长度前缀保证 FieldValue 与 PK 的边界无歧义：uvarint 是前缀码，
// 不同 FieldValue 的编码互不为前缀，等值前缀扫描（EncodeIndexPrefix）
// 不会误命中 "FieldValue 是目标值前缀" 的其他记录。
package encoding

import "encoding/binary"

// DataPrefix 数据行键空间的 tag 字节。
const DataPrefix = 0x00

// IndexPrefix 索引键空间的 tag 字节。
const IndexPrefix = 0xFF

// EncodeKey 将 TableHash 与 PrimaryKey 拼接为数据行物理 Key。
// 格式: [0x00][TableHash 8B 定宽][PrimaryKey]
func EncodeKey(tableHash uint64, pk []byte) []byte {
	buf := make([]byte, 1+8+len(pk))
	buf[0] = DataPrefix
	binary.BigEndian.PutUint64(buf[1:9], tableHash)
	copy(buf[9:], pk)
	return buf
}

// EncodeIndexKey 构造二级索引的物理 Key。
// fieldNum 是字段在 proto 定义中的编号（1-based）。
// 格式: [0xFF][TableHash 8B 定宽][FieldNum byte][ValueLen Uvarint][FieldValue][PK]
func EncodeIndexKey(tableHash uint64, fieldNum int32, fieldValue, pk []byte) []byte {
	var tmp [binary.MaxVarintLen64]byte
	buf := make([]byte, 0, 1+8+1+binary.MaxVarintLen64+len(fieldValue)+len(pk))
	buf = append(buf, IndexPrefix)
	binary.BigEndian.PutUint64(tmp[:8], tableHash)
	buf = append(buf, tmp[:8]...)
	buf = append(buf, byte(fieldNum))
	nVal := binary.PutUvarint(tmp[:], uint64(len(fieldValue)))
	buf = append(buf, tmp[:nVal]...)
	buf = append(buf, fieldValue...)
	buf = append(buf, pk...)
	return buf
}

// DecodeIndexKey 从索引 Key 中还原 TableHash、字段编号、字段值和主键。
// ValueLen 长度前缀使 fieldValue 与 pk 的切分无歧义。
// 返回的切片与 raw 共享底层数组，调用方不得修改。
func DecodeIndexKey(raw []byte) (tableHash uint64, fieldNum int32, fieldValue, pk []byte, err error) {
	if len(raw) < 10 || raw[0] != IndexPrefix {
		return 0, 0, nil, nil, ErrMalformedKey
	}
	tableHash = binary.BigEndian.Uint64(raw[1:9])
	pos := 9
	fieldNum = int32(raw[pos])
	pos++
	vLen, nVal := binary.Uvarint(raw[pos:])
	if nVal <= 0 {
		return 0, 0, nil, nil, ErrMalformedKey
	}
	pos += nVal
	if uint64(len(raw)-pos) < vLen {
		return 0, 0, nil, nil, ErrMalformedKey
	}
	fieldValue = raw[pos : pos+int(vLen)]
	pk = raw[pos+int(vLen):]
	return tableHash, fieldNum, fieldValue, pk, nil
}

// EncodeIndexPrefix 构造索引前缀（不含 PK 后缀），用于 Pebble 前缀迭代。
// ValueLen 前缀保证只有 FieldValue 完全相等的索引 Key 才匹配此前缀。
// 格式: [0xFF][TableHash 8B 定宽][FieldNum byte][ValueLen Uvarint][FieldValue]
func EncodeIndexPrefix(tableHash uint64, fieldNum int32, fieldValue []byte) []byte {
	var tmp [binary.MaxVarintLen64]byte
	buf := make([]byte, 0, 1+8+1+binary.MaxVarintLen64+len(fieldValue))
	buf = append(buf, IndexPrefix)
	binary.BigEndian.PutUint64(tmp[:8], tableHash)
	buf = append(buf, tmp[:8]...)
	buf = append(buf, byte(fieldNum))
	nVal := binary.PutUvarint(tmp[:], uint64(len(fieldValue)))
	buf = append(buf, tmp[:nVal]...)
	buf = append(buf, fieldValue...)
	return buf
}

// EncodeUniqueIndexKey 构造唯一索引的物理 Key。
// 与普通索引不同，唯一索引 Key 不携带 PK 后缀：同一字段值在键空间中至多存在
// 一个条目，重复值天然复用同一物理 Key；PK 存放在 Value 中。
// 这使得 Create/Update 可以用一次 O(1) Get 检查冲突，等值扫描也退化为点查。
// 布局与 EncodeIndexPrefix 相同: [0xFF][TableHash 8B 定宽][FieldNum byte][ValueLen Uvarint][FieldValue]
func EncodeUniqueIndexKey(tableHash uint64, fieldNum int32, fieldValue []byte) []byte {
	return EncodeIndexPrefix(tableHash, fieldNum, fieldValue)
}

// DecodeKey 从物理 Key 中还原 TableHash 与 PrimaryKey。
// 返回的 pk 切片与 raw 共享底层数组，调用方不得修改。
func DecodeKey(raw []byte) (tableHash uint64, pk []byte, err error) {
	if len(raw) < 9 || raw[0] != DataPrefix {
		return 0, nil, ErrMalformedKey
	}
	tableHash = binary.BigEndian.Uint64(raw[1:9])
	return tableHash, raw[9:], nil
}
