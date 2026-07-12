// 入口：protoc-gen-sqlitex protoc 插件。
// 按 protoc 插件协议，从 stdin 读取 CodeGeneratorRequest，输出 CodeGeneratorResponse 到 stdout。
// 为带有 (sqlitex.table) option 的 Message 生成强类型 Store、零反射序列化器、Fluent Query 和 Mock 实现。
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/mogumc/sqlitex/internal/codegen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

func main() {
	reqBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "protoc-gen-sqlitex: failed to read stdin: %v\n", err)
		os.Exit(1)
	}

	req := &pluginpb.CodeGeneratorRequest{}
	if err := proto.Unmarshal(reqBytes, req); err != nil {
		fmt.Fprintf(os.Stderr, "protoc-gen-sqlitex: failed to unmarshal request: %v\n", err)
		os.Exit(1)
	}

	resp := codegen.GenerateResponse(req)

	respBytes, err := proto.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "protoc-gen-sqlitex: failed to marshal response: %v\n", err)
		os.Exit(1)
	}

	if _, err := os.Stdout.Write(respBytes); err != nil {
		fmt.Fprintf(os.Stderr, "protoc-gen-sqlitex: failed to write stdout: %v\n", err)
		os.Exit(1)
	}
}
