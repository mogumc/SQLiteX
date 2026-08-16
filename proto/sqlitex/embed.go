// 将 sqlitex 的 options.proto 以原始文本形式内嵌，
// 供运行期解析用户 .proto 时解析自定义 option 扩展（如 sqlitex-admin），
// 与 protoc 编译路径共用同一份事实来源。
package sqlitexpb

import _ "embed"

// OptionsProtoText 是 sqlitex/options.proto 的原文。
//
//go:embed options.proto
var OptionsProtoText string
