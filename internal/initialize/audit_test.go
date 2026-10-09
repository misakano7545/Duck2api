//go:build audit

package initialize

import (
	"sync"
	"testing"
)

// P0 回归：fileStorage 曾被并发 HTTP handler 裸读写，两个同时到达的请求（或一个上传 +
// 一个带 file_id 的 chat）会让 Go 运行时抛出不可恢复的 **fatal error: concurrent map writes**
// —— 进程直接退出，gin 的 Recovery 接不住。修法是把 7 处访问收敛到
// storeFile / lookupFile / deleteFile 三个加了 RWMutex 的 helper。
// 这里跑同一套并发访问模式：崩了就是回归，没崩且数据一致才算过。
func TestAuditFileStorageConcurrent(t *testing.T) {
	const g, per = 8, 300

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < g; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			id := "audit-file-" + string(rune('a'+n))
			for j := 0; j < per; j++ {
				storeFile(&StoredFile{ID: id, Filename: "x", Bytes: make([]byte, 16)})
				if f, ok := lookupFile(id); !ok || f.ID != id {
					t.Errorf("写入后查不到: %s", id)
					return
				}
				// 模拟 filesList 的整表扫描
				fileStorageMu.RLock()
				for range fileStorage {
					break
				}
				fileStorageMu.RUnlock()
			}
			deleteFile(id)
			if _, ok := lookupFile(id); ok {
				t.Errorf("删除后仍能查到: %s", id)
			}
		}(i)
	}
	close(start)
	wg.Wait()
}

// deleteFile 把「存在性检查」和「删除」放在同一把写锁里，所以并发删同一个 id 只有一个能成功。
// 旧写法（先查再删）两步之间是 TOCTOU 窗口，两个请求会同时回 200 deleted。
func TestAuditDeleteFileIsAtomic(t *testing.T) {
	const g = 16
	storeFile(&StoredFile{ID: "audit-race-del"})

	var (
		mu   sync.Mutex
		wins int
	)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < g; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if deleteFile("audit-race-del") {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 {
		t.Fatalf("并发删除应恰好 1 次成功，实际 %d 次", wins)
	}
}
