package ratelimit

import (
	"math"
	"sync"
	"time"
)

// 令牌桶限流实现, 支持按速率回填令牌, 并提供预检和退款功能
type Bucket struct {
	mu       sync.Mutex
	capacity float64          // 桶的容量, 即最多可以存放的令牌数
	tokens   float64          // 当前桶中的令牌数
	perSec   float64          // 每秒钟回填的令牌数
	last     time.Time        // 上次回填令牌的时间
	now      func() time.Time // 可替换时钟计算函数, 主要用于测试
}

func NewBucket(capacity, perSec float64) *Bucket {
	return &Bucket{
		capacity: capacity,
		tokens:   capacity, // 一开始就设置满
		perSec:   perSec,
		last:     time.Now(),
		now:      time.Now,
	}
}

func (b *Bucket) Allow() bool {
	return b.AllowN(1)
}

func (b *Bucket) refillLocked(t time.Time) {
	if t.After(b.last) {
		b.tokens = math.Min(b.capacity, b.tokens+t.Sub(b.last).Seconds()*b.perSec)
		b.last = t
	}
}

// TODO: 这里使用了互斥锁, 可能会影响高并发性能，可以考虑使用无锁实现
func (b *Bucket) AllowN(n float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 先回填令牌
	b.refillLocked(b.now())
	if b.tokens < n {
		return false
	}

	b.tokens -= n
	return true
}

// 退款令牌, 将指定数量的令牌退回桶中
// 实际上超卖了也可以通过将n设置成负数来实现扣减令牌
func (b *Bucket) Refund(n float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked(b.now())
	b.tokens = math.Min(b.capacity, b.tokens+n)
}

func (b *Bucket) RetryAfter(n float64) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked(b.now())
	if b.tokens >= n {
		return 0
	}
	needed := n - b.tokens
	return int(math.Ceil(needed / b.perSec))
}
