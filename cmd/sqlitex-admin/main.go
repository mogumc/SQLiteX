// sqlitex-admin 是 SQLiteX 的轻量级 Web 管控面板（Phase 3）。
//
// 以只读模式加载一个 Pebble 数据目录，启动内嵌 HTTP Server，
// 提供可视化数据浏览（前缀扫描 + 游标分页）、单条记录十六进制检视、
// 存储统计与 Schema（.proto）查看，用于生产环境的数据调试。
//
// 用法：
//
//	sqlitex-admin -dir /path/to/db [-addr :8080] [-proto schema.proto]
package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

func main() {
	dir := flag.String("dir", "", "SQLiteX (Pebble) 数据目录，必填")
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP 监听地址（工具仅供本机调试，勿绑定到非回环地址）")
	protoPath := flag.String("proto", "", "可选：.proto Schema 文件路径，用于面板展示")
	maxBody := flag.Int64("maxbody", defaultMaxBodyBytes, "HTTP 请求体上限（字节），超大 proto 分片导入时可调大")
	flag.Parse()

	if *dir == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *maxBody <= 0 {
		log.Fatalf("invalid -maxbody %d: must be positive (default %d)", *maxBody, defaultMaxBodyBytes)
	}
	if err := warnIfExposed(*addr); err != nil {
		log.Printf("解析监听地址失败，按可能暴露处理: %v", err)
	}

	db, err := pebble.Open(*dir, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	srv := &adminServer{
		db:        db,
		dir:       *dir,
		protoPath: *protoPath,
		schema:    newSchemaStore(),
		maxBody:   *maxBody,
	}

	// 启动时自动导入 -proto 指定的 Schema（仅解析展示，失败不阻断启动）
	if *protoPath != "" {
		if data, err := os.ReadFile(*protoPath); err != nil {
			log.Printf("read proto: %v", err)
		} else if err := srv.schema.importProto(filepath.Base(*protoPath), string(data)); err != nil {
			log.Printf("import proto schema: %v", err)
		} else {
			log.Printf("schema imported: %s", *protoPath)
		}
	}

	uiFS, err := embeddedUI()
	if err != nil {
		log.Fatalf("embed ui: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", srv.handleUI(uiFS))
	mux.HandleFunc("/api/stats", srv.handleStats)
	mux.HandleFunc("/api/keys", srv.handleKeys)
	mux.HandleFunc("/api/key", srv.handleKey)
	mux.HandleFunc("/api/schema", srv.handleSchemaAPI)
	mux.HandleFunc("/schema", srv.handleSchemaText)

	// 请求体上限：防超大 multipart 溢写临时文件耗尽磁盘；
	// -maxbody 为预留的大分片导入接口（Schema 上传的 LimitReader 同步跟随）
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, srv.maxBody)
		mux.ServeHTTP(w, r)
	})

	log.Printf("sqlitex-admin listening on http://%s (dir=%s, maxbody=%d)", *addr, *dir, srv.maxBody)
	server := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second, // slowloris 防护
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

// defaultMaxBodyBytes 默认请求体上限：8MB（Schema 上传远超所需）。
const defaultMaxBodyBytes = 8 << 20

// warnIfExposed 监听地址绑定到非回环接口时打印显著警告。
// 本工具无认证且可读全库数据，仅设计为本机调试使用。
func warnIfExposed(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	target := host
	if target == "" {
		target = "0.0.0.0 (全部网卡)"
	}
	log.Printf("========================================================================")
	log.Printf("⚠ 安全警告: 监听地址 %s 绑定到非回环接口", target)
	log.Printf("⚠ 本工具无认证、可读取整个数据库内容，仅供本机调试使用！")
	log.Printf("⚠ 如需远程访问请自行加反向代理认证，风险自担")
	log.Printf("========================================================================")
	return nil
}

// adminServer 持有只读 Pebble 句柄与面板配置。
type adminServer struct {
	db        *pebble.DB
	dir       string
	protoPath string
	schema    *schemaStore
	maxBody   int64 // 请求体上限（-maxbody），Schema 上传分片同步跟随
}

// keyEntry 是 /api/keys 返回的单条记录摘要。
type keyEntry struct {
	Key     string      `json:"key"`               // 尽力可读形式（不可打印字符转 \xNN）
	KeyB64  string      `json:"key_b64"`           // 原始 key 的 base64，用作游标与详情查询
	Size    int         `json:"size"`              // value 字节数
	Preview string      `json:"preview"`           // value 可打印片段（≤64 字节）
	Decoded *decodedKey `json:"decoded,omitempty"` // schema 导入后的语义化解码
}

// keysResponse 是 /api/keys 的分页响应。
type keysResponse struct {
	Entries    []keyEntry `json:"entries"`
	NextCursor string     `json:"next_cursor,omitempty"` // 空表示没有更多数据
}

// statsResponse 是 /api/stats 的存储概览。
type statsResponse struct {
	Dir            string `json:"dir"`
	DiskUsageBytes uint64 `json:"disk_usage_bytes"` // 目录实际占用
	LiveBytes      uint64 `json:"live_bytes"`       // Pebble 逻辑活跃数据
	WALSizeBytes   uint64 `json:"wal_size_bytes"`
	MemTableBytes  uint64 `json:"memtable_bytes"`
	TableCount     int64  `json:"sstable_count"`
	ReadOnly       bool   `json:"read_only"`
}

// handleUI 服务嵌入的前端静态资源（web/dist）。
// 静态资源由 Vite 独立构建（见 web/README.md），go:embed 嵌入二进制。
// 未命中的路径回退到 index.html（SPA 语义）。
func (s *adminServer) handleUI(uiFS fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(uiFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/")
		if rel != "" {
			if _, err := fs.Stat(uiFS, rel); err != nil {
				r.URL.Path = "/" // SPA fallback：未知路径渲染入口页
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *adminServer) handleStats(w http.ResponseWriter, r *http.Request) {
	pm := s.db.Metrics()
	resp := statsResponse{
		Dir:            s.dir,
		DiskUsageBytes: dirSize(s.dir),
		LiveBytes:      uint64(pm.Total().Size),
		WALSizeBytes:   pm.WAL.PhysicalSize,
		MemTableBytes:  pm.MemTable.Size,
		TableCount:     sstableCount(s.dir),
		ReadOnly:       true,
	}
	writeJSON(w, resp)
}

// handleKeys 按 prefix 前缀扫描 + cursor 游标分页（O(1) Seek，与引擎侧一致）。
// 参数：prefix（原始字符串）、cursor（base64 的上页末 key）、limit（默认 50，上限 500）、
// table_id（表过滤：仅扫描该表数据行，与生成代码的表前缀扫描同一物理语义）。
func (s *adminServer) handleKeys(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n := atoiClamp(v, 1, 500); n > 0 {
			limit = n
		}
	}

	// 表级过滤：物理前缀 [TableID Uvarint]（uvarint 编码首字节天然无歧义，
	// 0xFF 索引空间不会落入任何表前缀，与生成代码 EncodeKey(tableID, nil) 一致）
	if v := r.URL.Query().Get("table_id"); v != "" {
		tableID, ok := parseUint64(v)
		if !ok {
			httpError(w, http.StatusBadRequest, fmt.Errorf("invalid table_id"))
			return
		}
		prefix = string(dataKeyPrefix(tableID))
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix)})
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	defer iter.Close()

	// 游标定位：O(1) Seek 到上页末 key 的后继
	if cur := r.URL.Query().Get("cursor"); cur != "" {
		last, err := base64.RawURLEncoding.DecodeString(cur)
		if err != nil {
			httpError(w, http.StatusBadRequest, fmt.Errorf("invalid cursor"))
			return
		}
		succ := make([]byte, len(last)+1)
		copy(succ, last)
		iter.SeekGE(succ)
	} else {
		iter.SeekGE([]byte(prefix))
	}

	resp := keysResponse{Entries: make([]keyEntry, 0, limit)}
	for ; iter.Valid(); iter.Next() {
		k := iter.Key()
		if !strings.HasPrefix(string(k), prefix) {
			break // 越过前缀边界
		}
		if len(resp.Entries) == limit {
			resp.NextCursor = lastKeyOf(resp.Entries)
			break
		}
		v := iter.Value()
		pv := v
		if len(pv) > 64 {
			pv = pv[:64]
		}
		entry := keyEntry{
			Key:     printable(k),
			KeyB64:  base64.RawURLEncoding.EncodeToString(k),
			Size:    len(v),
			Preview: printable(pv),
		}
		if d := s.schema.decodeKey(k); d.Kind != "unknown" {
			entry.Decoded = &d
		}
		resp.Entries = append(resp.Entries, entry)
	}
	writeJSON(w, resp)
}

// handleKey 返回单条记录的完整视图：原始十六进制转储 + schema 语义化解码。
// 参数：k（base64 编码的原始 key）。
func (s *adminServer) handleKey(w http.ResponseWriter, r *http.Request) {
	kb, err := base64.RawURLEncoding.DecodeString(r.URL.Query().Get("k"))
	if err != nil || len(kb) == 0 {
		httpError(w, http.StatusBadRequest, fmt.Errorf("invalid key"))
		return
	}
	val, closer, err := s.db.Get(kb)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	defer closer.Close()

	resp := map[string]any{
		"key_hex":   hex.EncodeToString(kb),
		"key_print": printable(kb),
		"size":      len(val),
	}
	resp["value_hex"], resp["value_print"], resp["value_truncated"] = truncateForDump(val)

	// schema 语义化：Key 归属 + Value 逐字段解码
	if d := s.schema.decodeKey(kb); d.Kind == "data" {
		if ts := s.schema.tableOf(kb); ts != nil {
			resp["decoded_key"] = d
			resp["decoded_value"] = s.schema.decodeValue(ts, val)
		}
	} else if d.Kind == "index" {
		// 索引键的 value 即 PK bytes（见生成代码 EncodeIndexKey 的写入侧）
		if ts := s.schema.tableOfIndex(kb); ts != nil {
			d.PK = decodePK(ts, val)
		}
		resp["decoded_key"] = d
	}
	writeJSON(w, resp)
}

// handleSchemaAPI 管理已导入的库表结构。
// GET → 当前 schema；POST(multipart file) → 导入 .proto 并解析；DELETE → 清空。
func (s *adminServer) handleSchemaAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		source, tables := s.schema.snapshot()
		writeJSON(w, map[string]any{"source": source, "tables": tables})
	case http.MethodPost:
		file, header, err := r.FormFile("file")
		if err != nil {
			// MaxBytesReader 触发的超限单独映射为 413，提示用 -maxbody 调大
			if strings.Contains(err.Error(), "request body too large") {
				httpError(w, http.StatusRequestEntityTooLarge,
					fmt.Errorf("request body exceeds -maxbody=%d, restart with a larger -maxbody for big imports", s.maxBody))
				return
			}
			httpError(w, http.StatusBadRequest, fmt.Errorf("multipart field 'file' required: %v", err))
			return
		}
		defer file.Close()
		// 文件内容上限与请求体上限（-maxbody）同步，保留大分片导入通道
		data, err := io.ReadAll(io.LimitReader(file, s.maxBody))
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		name := header.Filename
		if name == "" {
			name = "uploaded.proto"
		}
		if err := s.schema.importProto(name, string(data)); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		source, tables := s.schema.snapshot()
		writeJSON(w, map[string]any{"source": source, "tables": tables})
	case http.MethodDelete:
		s.schema.clear()
		writeJSON(w, map[string]any{"source": "", "tables": []any{}})
	default:
		httpError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
	}
}

// handleSchemaText 输出 .proto 原文（提供 -proto 时）。
func (s *adminServer) handleSchemaText(w http.ResponseWriter, r *http.Request) {
	if s.protoPath == "" {
		httpError(w, http.StatusNotFound, fmt.Errorf("no proto file configured (-proto)"))
		return
	}
	data, err := os.ReadFile(s.protoPath)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(data)
}

// ---- 工具函数 ----

// lastKeyOf 取分页结果末条 key（base64 解回原始字节，再重新编码）。
// 独立成函数是为了让 NextCursor 的来源清晰。
func lastKeyOf(entries []keyEntry) string {
	return entries[len(entries)-1].KeyB64
}

// printable 将字节串转为尽力可读形式：
// 可打印 ASCII/UTF-8 保留，其余转 \xNN。
func printable(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if (r == utf8.RuneError && size <= 1) || r < 0x20 || r == 0x7f {
			fmt.Fprintf(&sb, "\\x%02x", b[i])
			i++
			continue
		}
		sb.WriteRune(r)
		i += size
	}
	return sb.String()
}

// sstableCount 统计目录中 .sst 文件数（pebble v1.1.5 的 Metrics 未直接暴露该值）。
func sstableCount(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".sst") {
			n++
		}
		return nil
	})
	return n
}

// dirSize 递归统计数据目录实际磁盘占用。
func dirSize(dir string) uint64 {
	var total int64
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil // 单文件错误不中断统计
	})
	if total < 0 {
		return 0
	}
	return uint64(total)
}

func atoiClamp(s string, lo, hi int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func httpError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// parseUint64 解析非负整数查询参数。
func parseUint64(s string) (uint64, bool) {
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// maxValueDumpBytes / maxValuePrintBytes 原始字节转储的展示上限。
// 面向「大二进制字段」场景：MB 级 blob 全量 hex 会使 JSON 膨胀 2 倍并卡顿浏览器，
// 超限时截断并在响应中标记 value_truncated，结构化解码不受影响。
const (
	maxValueDumpBytes  = 256 << 10 // hex 转储上限 256KB
	maxValuePrintBytes = 4 << 10   // 可读预览上限 4KB
)

// truncateForDump 生成 value 的展示转储（hex + 可读形式），超限截断。
func truncateForDump(val []byte) (hexStr, printStr string, truncated bool) {
	dumpVal := val
	printVal := val
	if len(val) > maxValueDumpBytes {
		dumpVal = val[:maxValueDumpBytes]
		printVal = val[:maxValuePrintBytes]
		truncated = true
	} else if len(val) > maxValuePrintBytes {
		printVal = val[:maxValuePrintBytes]
	}
	return hex.EncodeToString(dumpVal), printable(printVal), truncated
}
