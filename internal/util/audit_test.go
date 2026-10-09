//go:build audit

package util

import (
	"math/rand"
	"regexp"
	"sync"
	"testing"
)

// P3 回归：RandomHexadecimalString 原实现每次调用都 rand.Seed(time.Now().UnixNano())，
// 而 Seed 改的是**全局共享**随机源 —— 并发下「A 播种 → B 播种 → A 抽数」会让 A 拿到 B 的
// 序列，同一纳秒内的调用必然撞号。它是 tool_call id / message id 的来源，撞号意味着客户端
// 把两次调用认成同一个。现在改用 crypto/rand。
func TestAuditRandomHexID(t *testing.T) {
	// 形状：16 个十六进制字符（调用方按定长拼 id，长度不能变）
	shape := regexp.MustCompile(`^[0-9a-f]{16}$`)
	for i := 0; i < 1000; i++ {
		if s := RandomHexadecimalString(); !shape.MatchString(s) {
			t.Fatalf("形状不符: %q", s)
		}
	}

	// 并发唯一性
	const g, per = 8, 20000
	var mu sync.Mutex
	all := make(map[string]int, g*per)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < g; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			local := make([]string, 0, per)
			for j := 0; j < per; j++ {
				local = append(local, RandomHexadecimalString())
			}
			mu.Lock()
			for _, s := range local {
				all[s]++
			}
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	if len(all) != g*per {
		t.Fatalf("并发下有 ID 撞号: 产生 %d 个, 唯一 %d 个", g*per, len(all))
	}
}

// 回归的核心：新实现不再碰全局 math/rand。旧实现会在调用里重新播种全局源，于是任何依赖
// math/rand 全局序列的代码都会被打乱。
//
// 但 Go 1.24 起顶层 Seed 默认是 no-op（GODEBUG=randseednop=0 可恢复），本机工具链就是这个
// 行为 —— 这恰好解释了当初实测「单线程 5 万次 / 并发 16 万次 0 碰撞」：全局源根本没被重新
// 播种，所以那条路在本工具链上测不出来。先把这个事实固定下来，免得把 0 碰撞误读成旧实现没问题。
func TestAuditRandomHexDoesNotTouchGlobalRand(t *testing.T) {
	rand.Seed(1)
	a := rand.Int63()
	rand.Seed(1)
	b := rand.Int63()
	if a != b {
		t.Log("本工具链上 math/rand 顶层 Seed 已是 no-op ⇒ 旧实现「重新播种全局源」这条无法在此证伪；" +
			"它的危害只在 Go<1.24 或 GODEBUG=randseednop=0 时显现。crypto/rand 从构造上消除该依赖。")
		return
	}
	// Seed 生效：可以真正验证旧实现会打乱全局序列。
	rand.Seed(1)
	want := rand.Int63()
	rand.Seed(1)
	_ = RandomHexadecimalString()
	if got := rand.Int63(); got != want {
		t.Fatalf("RandomHexadecimalString 仍在改全局 math/rand 源: 期望 %d, 得到 %d", want, got)
	}
}
