package encoding

import "errors"

// ErrMalformedKey 表示物理 Key 无法解析为合法结构。
var ErrMalformedKey = errors.New("sqlitex/encoding: malformed key")
