// Package main 提供 SQLiteX 的独立压测工具。
//
// 用途：
// - 验证高并发 Put/Get 性能
// - 测量不同 value 大小下的吞吐量
// - 监控内存占用与背压触发情况
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/mogumc/sqlitex"
)

func main() {
	dir := flag.String("dir", "./bench-data", "数据目录")
	workers := flag.Int("workers", 8, "并发工作者数量")
	ops := flag.Int("ops", 10000, "每个 worker 的操作次数")
	valueSize := flag.Int("value-size", 128, "Value 大小（字节）")
	queueLen := flag.Int("queue-len", 4096, "写队列长度")
	asyncWAL := flag.Bool("async-wal", false, "启用异步 WAL（NoSync 写入）")
	walBytesPerSync := flag.Int("wal-bytes-per-sync", 0, "WAL 后台 sync 字节间隔（0=不启用）")
	walMinSyncInterval := flag.Duration("wal-min-sync-interval", 0, "WAL 最小 sync 间隔")
	batchCommitSize := flag.Int("batch-commit-size", 0, "组提交批量大小（0=逐条写入）")
	flag.Parse()

	// 清理旧数据
	os.RemoveAll(*dir)

	db, err := sqlitex.Open(sqlitex.Config{
		Dir:                *dir,
		MaxQueueLen:        *queueLen,
		AsyncWAL:           *asyncWAL,
		WALBytesPerSync:    *walBytesPerSync,
		WALMinSyncInterval: *walMinSyncInterval,
		BatchCommitSize:    *batchCommitSize,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Open failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	fmt.Println("=== SQLiteX Benchmark ===")
	fmt.Printf("Workers: %d, Ops/Worker: %d, ValueSize: %d bytes\n", *workers, *ops, *valueSize)
	if *asyncWAL {
		fmt.Printf("WAL: async (NoSync), BytesPerSync: %d, MinSyncInterval: %v\n",
			*walBytesPerSync, *walMinSyncInterval)
	} else {
		fmt.Println("WAL: sync (每写 fsync)")
	}
	if *batchCommitSize > 0 {
		fmt.Printf("Batch Commit: %d ops/batch\n", *batchCommitSize)
	} else {
		fmt.Println("Batch Commit: disabled (逐条写入)")
	}
	fmt.Println()

	// 生成固定大小的 value
	value := make([]byte, *valueSize)
	for i := range value {
		value[i] = byte(i % 256)
	}

	// Phase 1: 纯写入压测
	fmt.Println("--- Phase 1: 纯写入 (Put) ---")
	runPutBench(db, *workers, *ops, value)

	// Phase 2: 纯读取压测
	fmt.Println("\n--- Phase 2: 纯读取 (Get) ---")
	runGetBench(db, *workers, *ops)

	// Phase 3: 混合读写（70% 读 + 30% 写）
	fmt.Println("\n--- Phase 3: 混合读写 (70% Read / 30% Write) ---")
	runMixedBench(db, *workers, *ops, value, 0.7)

	// 输出最终内存统计
	fmt.Println("\n--- Final Memory Stats ---")
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("Alloc: %d MB, Sys: %d MB, NumGC: %d\n",
		m.Alloc>>20, m.Sys>>20, m.NumGC)

	// 清理
	os.RemoveAll(*dir)
}

func runPutBench(db *sqlitex.DB, workers, ops int, value []byte) {
	var wg sync.WaitGroup
	throttled := 0
	var throttledMu sync.Mutex

	start := time.Now()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := []byte(fmt.Sprintf("w%d-k%d", workerID, i))
				err := db.Put(key, value)
				if err != nil {
					if err == sqlitex.ErrWriteThrottled {
						throttledMu.Lock()
						throttled++
						throttledMu.Unlock()
					} else {
						fmt.Fprintf(os.Stderr, "Put error: %v\n", err)
					}
				}
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)
	totalOps := workers * ops
	qps := float64(totalOps) / elapsed.Seconds()

	fmt.Printf("Total Ops: %d, Elapsed: %v\n", totalOps, elapsed)
	fmt.Printf("QPS: %.0f ops/sec\n", qps)
	fmt.Printf("Throttled: %d ops (%.2f%%)\n", throttled, float64(throttled)/float64(totalOps)*100)
}

func runGetBench(db *sqlitex.DB, workers, ops int) {
	var wg sync.WaitGroup
	misses := 0
	var missesMu sync.Mutex

	start := time.Now()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := []byte(fmt.Sprintf("w%d-k%d", workerID, i))
				val, err := db.Get(key)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Get error: %v\n", err)
					return
				}
				if val == nil {
					missesMu.Lock()
					misses++
					missesMu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)
	totalOps := workers * ops
	qps := float64(totalOps) / elapsed.Seconds()

	fmt.Printf("Total Ops: %d, Elapsed: %v\n", totalOps, elapsed)
	fmt.Printf("QPS: %.0f ops/sec\n", qps)
	fmt.Printf("Misses: %d ops (%.2f%%)\n", misses, float64(misses)/float64(totalOps)*100)
}

func runMixedBench(db *sqlitex.DB, workers, ops int, value []byte, readRatio float64) {
	var wg sync.WaitGroup
	readOps := 0
	writeOps := 0
	var opsMu sync.Mutex

	start := time.Now()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := []byte(fmt.Sprintf("w%d-k%d", workerID, i))

				// 根据比例决定读还是写
				if float64(i%100)/100 < readRatio {
					_, err := db.Get(key)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Get error: %v\n", err)
						return
					}
					opsMu.Lock()
					readOps++
					opsMu.Unlock()
				} else {
					err := db.Put(key, value)
					if err != nil {
						fmt.Fprintf(os.Stderr, "Put error: %v\n", err)
						return
					}
					opsMu.Lock()
					writeOps++
					opsMu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)
	totalOps := readOps + writeOps
	qps := float64(totalOps) / elapsed.Seconds()

	fmt.Printf("Total Ops: %d (Read: %d, Write: %d), Elapsed: %v\n", totalOps, readOps, writeOps, elapsed)
	fmt.Printf("QPS: %.0f ops/sec\n", qps)
	fmt.Printf("Read Ratio: %.1f%%, Write Ratio: %.1f%%\n",
		float64(readOps)/float64(totalOps)*100,
		float64(writeOps)/float64(totalOps)*100)
}
