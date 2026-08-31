package codegen

import (
	"testing"
	"time"

	sqlitexpb "github.com/mogumc/sqlitex/proto/sqlitex"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func TestProtoTypeToGo(t *testing.T) {
	tests := []struct {
		protoType descriptorpb.FieldDescriptorProto_Type
		want      string
	}{
		{descriptorpb.FieldDescriptorProto_TYPE_INT64, "int64"},
		{descriptorpb.FieldDescriptorProto_TYPE_UINT64, "uint64"},
		{descriptorpb.FieldDescriptorProto_TYPE_STRING, "string"},
		{descriptorpb.FieldDescriptorProto_TYPE_BYTES, "[]byte"},
		{descriptorpb.FieldDescriptorProto_TYPE_BOOL, "bool"},
		{descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, "float64"},
		{descriptorpb.FieldDescriptorProto_TYPE_FLOAT, "float32"},
		{descriptorpb.FieldDescriptorProto_TYPE_INT32, "int32"},
		{descriptorpb.FieldDescriptorProto_TYPE_ENUM, "int32"},
	}

	for _, tt := range tests {
		got, err := protoTypeToGo(tt.protoType)
		if err != nil {
			t.Errorf("protoTypeToGo(%v) error: %v", tt.protoType, err)
			continue
		}
		if got != tt.want {
			t.Errorf("protoTypeToGo(%v) = %q, want %q", tt.protoType, got, tt.want)
		}
	}
}

func TestResolveGoPackage(t *testing.T) {
	tests := []struct {
		name  string
		goPkg string
		want  string
	}{
		{"full path with semicolon", "github.com/mogumc/sqlitex/generated;genpkg", "genpkg"},
		{"full path no semicolon", "github.com/mogumc/sqlitex/generated", "generated"},
		{"simple name", "mypackage", "mypackage"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &descriptorpb.FileDescriptorProto{}
			if tt.goPkg != "" {
				file.Options = &descriptorpb.FileOptions{
					GoPackage: strPtr(tt.goPkg),
				}
			}
			got := resolveGoPackage(file)
			if got != tt.want {
				t.Errorf("resolveGoPackage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShouldCompress(t *testing.T) {
	tests := []struct {
		name          string
		field         *FieldIR
		tableCompress bool
		threshold     int32
		want          bool
	}{
		{
			name:          "string with table compress on",
			field:         &FieldIR{ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Compress: false},
			tableCompress: true,
			threshold:     256,
			want:          true,
		},
		{
			name:          "bytes with field compress explicit on",
			field:         &FieldIR{ProtoType: descriptorpb.FieldDescriptorProto_TYPE_BYTES, Compress: true},
			tableCompress: false,
			threshold:     256,
			want:          true,
		},
		{
			name:          "int64 not compressible",
			field:         &FieldIR{ProtoType: descriptorpb.FieldDescriptorProto_TYPE_INT64, Compress: false},
			tableCompress: true,
			threshold:     256,
			want:          false,
		},
		{
			name:          "repeated string not compressible",
			field:         &FieldIR{ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, IsRepeated: true, Compress: false},
			tableCompress: true,
			threshold:     256,
			want:          false,
		},
		{
			name:          "string with table compress off and no field override",
			field:         &FieldIR{ProtoType: descriptorpb.FieldDescriptorProto_TYPE_STRING, Compress: false},
			tableCompress: false,
			threshold:     256,
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldCompress(tt.field, tt.tableCompress, tt.threshold)
			if got != tt.want {
				t.Errorf("shouldCompress() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractTableOption(t *testing.T) {
	// 无 Option
	msg := &descriptorpb.DescriptorProto{
		Name: strPtr("NoOpt"),
	}
	if got := extractTableOption(msg); got != nil {
		t.Errorf("expected nil for message without option")
	}

	// 有 Options 但无 table 扩展
	msg2 := &descriptorpb.DescriptorProto{
		Name:    strPtr("WithOpt"),
		Options: &descriptorpb.MessageOptions{},
	}
	if got := extractTableOption(msg2); got != nil {
		t.Errorf("expected nil for message with options but no table extension")
	}
}

func TestBuildFieldIR(t *testing.T) {
	field := &descriptorpb.FieldDescriptorProto{
		Name:   strPtr("user_id"),
		Number: intPtr(1),
		Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_INT64),
		Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
	}

	fir, err := buildFieldIR(field)
	if err != nil {
		t.Fatalf("buildFieldIR() error: %v", err)
	}
	if fir.Name != "user_id" {
		t.Errorf("Name = %q, want %q", fir.Name, "user_id")
	}
	if fir.GoType != "int64" {
		t.Errorf("GoType = %q, want %q", fir.GoType, "int64")
	}
	if fir.IsPrimaryKey {
		t.Error("IsPrimaryKey should be false")
	}
	if fir.IsRepeated {
		t.Error("IsRepeated should be false")
	}
}

func TestBuildFieldIRRepeated(t *testing.T) {
	field := &descriptorpb.FieldDescriptorProto{
		Name:   strPtr("tags"),
		Number: intPtr(2),
		Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
		Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_REPEATED),
	}

	fir, err := buildFieldIR(field)
	if err != nil {
		t.Fatalf("buildFieldIR() error: %v", err)
	}
	if !fir.IsRepeated {
		t.Error("expected IsRepeated=true")
	}
	if fir.GoType != "[]string" {
		t.Errorf("GoType = %q, want %q", fir.GoType, "[]string")
	}
}

// helpers
func strPtr(s string) *string { return &s }
func intPtr(n int32) *int32   { return &n }
func typPtr(t descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto_Type {
	return &t
}
func labelPtr(l descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto_Label {
	return &l
}

func TestParseTTL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"seconds", "30s", 30 * time.Second, false},
		{"minutes", "5m", 5 * time.Minute, false},
		{"hours", "24h", 24 * time.Hour, false},
		{"days", "7d", 7 * 24 * time.Hour, false},
		{"milliseconds", "500ms", 500 * time.Millisecond, false},
		{"zero", "0s", 0, true},
		{"negative", "-5m", 0, true},
		{"negative day", "-1d", 0, true},
		{"bad day value", "abc d", 0, true},
		{"empty", "", 0, true},
		{"garbage", "abc", 0, true},
		{"bad day suffix", "1.5d", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTTL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseTTL(%q) expected error, got %v", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("parseTTL(%q) unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("parseTTL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestBuildTableIRTTL 验证 buildTableIR 从字段 ttl option 提取记录级 TTL。
func TestBuildTableIRTTL(t *testing.T) {
	msg := &descriptorpb.DescriptorProto{
		Name: strPtr("Session"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name:   strPtr("id"),
				Number: intPtr(1),
				Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_INT64),
				Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
			},
			{
				Name:   strPtr("token"),
				Number: intPtr(2),
				Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
				Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
			},
		},
	}
	tableOpt := &sqlitexpb.TableOption{PrimaryKey: "id"}

	// 无 TTL
	table, err := buildTableIR(msg, tableOpt, "genpkg", 1)
	if err != nil {
		t.Fatalf("buildTableIR no-ttl: %v", err)
	}
	if table.HasTTL {
		t.Error("expected HasTTL=false without ttl option")
	}

	// 带 TTL option（通过 field option 注入）
	msg.Field[1].Options = &descriptorpb.FieldOptions{}
	opt := &sqlitexpb.FieldOption{Index: sqlitexpb.IndexOption_INDEX_NONE}
	opt.Ttl = "1s"
	proto.SetExtension(msg.Field[1].Options, sqlitexpb.E_Field, opt)

	table, err = buildTableIR(msg, tableOpt, "genpkg", 1)
	if err != nil {
		t.Fatalf("buildTableIR with ttl: %v", err)
	}
	if !table.HasTTL {
		t.Fatal("expected HasTTL=true with ttl option")
	}
	if table.TTL != time.Second {
		t.Errorf("TTL = %v, want %v", table.TTL, time.Second)
	}

	// 非法 TTL 应报错
	m := &descriptorpb.DescriptorProto{
		Name:  strPtr("Bad"),
		Field: msg.Field,
	}
	badOpt := &sqlitexpb.FieldOption{Index: sqlitexpb.IndexOption_INDEX_NONE}
	badOpt.Ttl = "-1s"
	badField := &descriptorpb.FieldDescriptorProto{
		Name:    strPtr("token"),
		Number:  intPtr(2),
		Type:    typPtr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
		Label:   labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
		Options: &descriptorpb.FieldOptions{},
	}
	proto.SetExtension(badField.Options, sqlitexpb.E_Field, badOpt)
	m.Field[1] = badField
	if _, err := buildTableIR(m, tableOpt, "genpkg", 1); err == nil {
		t.Error("expected error for negative TTL")
	}
}

// TestBuildTableIRRepeatedField 验证 Phase 1 拒绝 repeated 字段。
func TestBuildTableIRRepeatedField(t *testing.T) {
	msg := &descriptorpb.DescriptorProto{
		Name: strPtr("User"),
		Field: []*descriptorpb.FieldDescriptorProto{
			{
				Name:   strPtr("id"),
				Number: intPtr(1),
				Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_INT64),
				Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL),
			},
			{
				Name:   strPtr("tags"),
				Number: intPtr(2),
				Type:   typPtr(descriptorpb.FieldDescriptorProto_TYPE_STRING),
				Label:  labelPtr(descriptorpb.FieldDescriptorProto_LABEL_REPEATED), // repeated 字段
			},
		},
	}
	tableOpt := &sqlitexpb.TableOption{PrimaryKey: "id"}

	_, err := buildTableIR(msg, tableOpt, "genpkg", 1)
	if err == nil {
		t.Fatal("expected error for repeated field, got nil")
	}
	if !containsSubstring(err.Error(), "repeated fields are not supported") {
		t.Errorf("error message = %q, want substring %q", err.Error(), "repeated fields are not supported")
	}
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
