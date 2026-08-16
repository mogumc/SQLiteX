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
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

func main() {
	dir := flag.String("dir", "", "SQLiteX (Pebble) 数据目录，必填")
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP 监听地址")
	protoPath := flag.String("proto", "", "可选：.proto Schema 文件路径，用于面板展示")
	flag.Parse()

	if *dir == "" {
		flag.Usage()
		os.Exit(2)
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
	mux.HandleFunc("/schema", srv.handleSchema)

	log.Printf("sqlitex-admin listening on http://%s (dir=%s)", *addr, *dir)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// adminServer 持有只读 Pebble 句柄与面板配置。
type adminServer struct {
	db        *pebble.DB
	dir       string
	protoPath string
}

// keyEntry 是 /api/keys 返回的单条记录摘要。
type keyEntry struct {
	Key     string `json:"key"`     // 尽力可读形式（不可打印字符转 \xNN）
	KeyB64  string `json:"key_b64"` // 原始 key 的 base64，用作游标与详情查询
	Size    int    `json:"size"`    // value 字节数
	Preview string `json:"preview"` // value 可打印片段（≤64 字节）
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
// 参数：prefix（原始字符串）、cursor（base64 的上页末 key）、limit（默认 50，上限 500）。
func (s *adminServer) handleKeys(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n := atoiClamp(v, 1, 500); n > 0 {
			limit = n
		}
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
		resp.Entries = append(resp.Entries, keyEntry{
			Key:     printable(k),
			KeyB64:  base64.RawURLEncoding.EncodeToString(k),
			Size:    len(v),
			Preview: printable(pv),
		})
	}
	writeJSON(w, resp)
}

// handleKey 返回单条记录的完整十六进制转储。
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
	writeJSON(w, map[string]any{
		"key_hex":     hex.EncodeToString(kb),
		"key_print":   printable(kb),
		"size":        len(val),
		"value_hex":   hex.EncodeToString(val),
		"value_print": printable(val),
	})
}

// handleSchema 输出 .proto 原文（提供 -proto 时）。
func (s *adminServer) handleSchema(w http.ResponseWriter, r *http.Request) {
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
