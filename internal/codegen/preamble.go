package codegen

// GeneratePreamble 生成一次性公共辅助代码（zstd 编解码、条件解析）。
// 这些符号是包级全局，多表同包时只允许存在一份，故抽取为独立 preamble，
// 由 Generator 在合并输出时仅注入一次。返回内容不含 package/import 声明。
func GeneratePreamble() string {
	return preambleBody
}

const preambleBody = `
var (
	_zstdEnc *zstd.Encoder
	_zstdDec *zstd.Decoder
)

func init() {
	var err error
	_zstdEnc, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil { panic(err) }
	_zstdDec, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
	if err != nil { panic(err) }
}

func _compressZstd(src []byte) []byte {
	return _zstdEnc.EncodeAll(src, nil)
}

func _decompressZstd(src []byte) ([]byte, error) {
	return _zstdDec.DecodeAll(src, nil)
}

func splitWhere(cond string) []string {
	lastSpace := -1
	for i := len(cond) - 1; i >= 0; i-- { if cond[i] == ' ' { lastSpace = i; break } }
	if lastSpace < 0 { return nil }
	inner := cond[:lastSpace]
	for i := len(inner) - 1; i >= 0; i-- { if inner[i] == ' ' { return []string{inner[:i], inner[i+1:]} } }
	return nil
}
`