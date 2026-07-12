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
