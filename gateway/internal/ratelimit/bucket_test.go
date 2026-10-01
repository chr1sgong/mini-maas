package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func newTestBucket(capacity, perSec float64) (*Bucket, *time.Time) {
	fakeNow := time.Now()
	b := NewBucket(capacity, perSec)
	b.last = fakeNow
	b.now = func() time.Time { return fakeNow }
	return b, &fakeNow
}

// 单测 1：按速率补充，且封顶容量
func TestRefillRateAndCap(t *testing.T) {
	b, now := newTestBucket(10, 5)
	b.tokens = 0
	*now = now.Add(time.Second) // 补 5 个
	if !b.AllowN(5) {
		t.Fatal("1s 后应有 5 个令牌")
	}
	*now = now.Add(100 * time.Second) // 补充远超容量，应封顶 10
	if !b.AllowN(10) {
		t.Fatal("补充应封顶到容量")
	}
	if b.AllowN(1) {
		t.Fatal("超过容量部分应被溢出丢弃")
	}
}

// 单测 2：余额不足拒绝且余额不变；拒绝不消耗令牌
func TestAllowNInsufficientLeavesBalance(t *testing.T) {
	b, _ := newTestBucket(10, 1)
	if b.AllowN(11) {
		t.Fatal("余额不足应拒绝")
	}
	if !b.AllowN(10) {
		t.Fatal("被拒的请求不得消耗令牌，余额应保持 10")
	}
}

// 单测 3：回填封顶容量；负数回填（补扣）允许余额变负
func TestRefundCapAndNegative(t *testing.T) {
	b, _ := newTestBucket(10, 1)
	b.Refund(100) // 应封顶 10，不能把桶灌爆
	if !b.AllowN(10) || b.AllowN(1) {
		t.Fatal("回填应封顶容量")
	}
	b.Refund(-15) // 补扣后余额 -15
	if b.AllowN(1) {
		t.Fatal("负余额期间预检应失败（欠债偿还中）")
	}
}

// 建议项：并发收发，go test -race 验证
func TestBucketConcurrent(t *testing.T) {
	b := NewBucket(1000, 1000)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				b.AllowN(1)
				b.Refund(1)
			}
		}()
	}
	wg.Wait()
}
