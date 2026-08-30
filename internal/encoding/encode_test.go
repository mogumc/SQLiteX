package encoding

import (
	"bytes"
	"testing"
)

func TestEncodeKey_Roundtrip(t *testing.T) {
	tests := []struct {
		name    string
		tableID uint64
		pk      []byte
	}{
		{"zero table", 0, []byte("pk-001")},
		{"small table", 1, []byte("pk-abc")},
		{"large table", 1<<63 - 1, []byte("pk-max")},
		{"empty pk", 42, []byte{}},
		{"binary pk", 7, []byte{0x00, 0xff, 0x80, 0x01}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeKey(tt.tableID, tt.pk)
			gotID, gotPK, err := DecodeKey(encoded)
			if err != nil {
				t.Fatalf("DecodeKey err: %v", err)
			}
			if gotID != tt.tableID {
				t.Errorf("tableID: got %d, want %d", gotID, tt.tableID)
			}
			if !bytes.Equal(gotPK, tt.pk) {
				t.Errorf("pk: got %x, want %x", gotPK, tt.pk)
			}
		})
	}
}

func TestDecodeKey_Malformed(t *testing.T) {
	// 截断的 uvarint：最高位全为1，永远读不完
	_, _, err := DecodeKey([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80})
	if err != ErrMalformedKey {
		t.Errorf("expected ErrMalformedKey, got %v", err)
	}
}

func TestDecodeKey_Empty(t *testing.T) {
	_, _, err := DecodeKey([]byte{})
	if err != ErrMalformedKey {
		t.Errorf("expected ErrMalformedKey, got %v", err)
	}
}

// TestEncodeKey_LexicographicOrder 验证同一 TableID 下，
// 不同 PrimaryKey 的物理 Key 保持字典序排列。
func TestEncodeKey_LexicographicOrder(t *testing.T) {
	tableID := uint64(1)
	pks := [][]byte{
		[]byte("aaa"),
		[]byte("aab"),
		[]byte("bbb"),
		[]byte("ccc"),
	}

	for i := 0; i < len(pks)-1; i++ {
		a := EncodeKey(tableID, pks[i])
		b := EncodeKey(tableID, pks[i+1])
		if bytes.Compare(a, b) >= 0 {
			t.Errorf("expected %x < %x", a, b)
		}
	}
}

// TestEncodeKey_TableIsolation 验证不同 TableID 的 Key 前缀互不相同。
func TestEncodeKey_TableIsolation(t *testing.T) {
	pk := []byte("same-pk")
	a := EncodeKey(1, pk)
	b := EncodeKey(2, pk)

	if bytes.HasPrefix(a, b[:len(a)]) || bytes.HasPrefix(b, a[:len(b)]) {
		t.Error("different TableIDs should not produce prefix-conflicting keys")
	}
}

// TestEncodeIndexKey_Roundtrip 验证索引键编解码互逆，
// 覆盖变长/空/二进制 fieldValue 与空 pk 等边界。
func TestEncodeIndexKey_Roundtrip(t *testing.T) {
	tests := []struct {
		name       string
		tableID    uint64
		fieldNum   int32
		fieldValue []byte
		pk         []byte
	}{
		{"string email", 1, 3, []byte("a@b.com"), []byte{1, 0, 0, 0, 0, 0, 0, 0}},
		{"empty value", 1, 3, []byte{}, []byte{2, 0, 0, 0, 0, 0, 0, 0}},
		{"empty pk", 2, 1, []byte("val"), []byte{}},
		{"both empty", 2, 1, []byte{}, []byte{}},
		{"binary value", 7, 200, []byte{0x00, 0xff, 0x80, 0x01}, []byte("pk")},
		{"large tableID", 1<<63 - 1, 3, []byte("v"), []byte("pk")},
		{"long value", 1, 3, bytes.Repeat([]byte("x"), 300), []byte("pk")}, // ValueLen 走 2 字节 uvarint
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeIndexKey(tt.tableID, tt.fieldNum, tt.fieldValue, tt.pk)
			gotID, gotFieldNum, gotValue, gotPK, err := DecodeIndexKey(encoded)
			if err != nil {
				t.Fatalf("DecodeIndexKey err: %v", err)
			}
			if gotID != tt.tableID {
				t.Errorf("tableID: got %d, want %d", gotID, tt.tableID)
			}
			if gotFieldNum != tt.fieldNum {
				t.Errorf("fieldNum: got %d, want %d", gotFieldNum, tt.fieldNum)
			}
			if !bytes.Equal(gotValue, tt.fieldValue) {
				t.Errorf("fieldValue: got %x, want %x", gotValue, tt.fieldValue)
			}
			if !bytes.Equal(gotPK, tt.pk) {
				t.Errorf("pk: got %x, want %x", gotPK, tt.pk)
			}
		})
	}
}

// TestEncodeIndexKey_ValuePKDisambiguation 回归测试：旧编码 [FieldValue][PK]
// 无长度分隔，("ab","c") 与 ("a","bc") 产生同一物理 Key，等值扫描互相污染。
// 新编码必须保证不同 FieldValue（互为前缀）的 Key 互不相同。
func TestEncodeIndexKey_ValuePKDisambiguation(t *testing.T) {
	a := EncodeIndexKey(1, 3, []byte("ab"), []byte("c"))
	b := EncodeIndexKey(1, 3, []byte("a"), []byte("bc"))
	if bytes.Equal(a, b) {
		t.Fatalf("distinct (value,pk) pairs produced identical keys: %x", a)
	}
}

// TestEncodeIndexPrefix_NoFalsePositive 回归测试：等值前缀扫描不得误命中
// "FieldValue 是目标值前缀" 的记录。修复前查询 value="a" 会扫出 value="ab" 的条目。
func TestEncodeIndexPrefix_NoFalsePositive(t *testing.T) {
	values := [][]byte{
		[]byte(""),
		[]byte("a"),
		[]byte("ab"),
		[]byte("abc"),
		[]byte{0x00},
		[]byte{0x00, 0x01},
	}
	pk := []byte("some-pk")

	for _, v1 := range values {
		prefix := EncodeIndexPrefix(1, 3, v1)

		// 同值 Key 必须命中前缀（正向语义保持）
		own := EncodeIndexKey(1, 3, v1, pk)
		if !bytes.HasPrefix(own, prefix) {
			t.Errorf("value %q: own key %x does not match own prefix %x", v1, own, prefix)
		}

		// 其他值的 Key 不得命中前缀（等值扫描无假阳性）
		for _, v2 := range values {
			if bytes.Equal(v1, v2) {
				continue
			}
			other := EncodeIndexKey(1, 3, v2, pk)
			if bytes.HasPrefix(other, prefix) {
				t.Errorf("prefix scan for value %q falsely matches key of value %q", v1, v2)
			}
		}
	}
}

// TestDecodeIndexKey_Malformed 验证截断/伪造的索引键返回 ErrMalformedKey。
func TestDecodeIndexKey_Malformed(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{"empty", []byte{}},
		{"missing prefix", []byte{0x01, 0x02, 0x03}},
		{"truncated after prefix", []byte{0xFF}},
		{"no fieldNum", []byte{0xFF, 0x01}},
		{"no valueLen", []byte{0xFF, 0x01, 0x03}},
		{"valueLen overruns", []byte{0xFF, 0x01, 0x03, 0x0A, 'x'}}, // 声明 10 字节只有 1 字节
		{"truncated uvarint tableID", []byte{0xFF, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, _, err := DecodeIndexKey(tt.raw)
			if err != ErrMalformedKey {
				t.Errorf("expected ErrMalformedKey, got %v", err)
			}
		})
	}
}
