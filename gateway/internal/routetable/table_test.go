package routetable

import (
	"slices"
	"sync"
	"testing"
)

func TestTableReplaceAndRead(t *testing.T) {
	table := NewTable()
	if got := table.Endpoints("qwen"); len(got) != 0 {
		t.Fatalf("empty table endpoints = %v, want empty", got)
	}

	table.Replace(map[string][]string{
		"qwen":  {"a:8000", "b:8000"},
		"empty": {},
	})

	if got := table.Endpoints("qwen"); !slices.Equal(got, []string{"a:8000", "b:8000"}) {
		t.Fatalf("endpoints = %v", got)
	}
	// 已注册但无就绪端：key 存在、列表为空（/v1/models 要列出它）
	if got := table.Endpoints("empty"); got == nil || len(got) != 0 {
		t.Fatalf("registered-empty endpoints = %v, want non-nil empty", got)
	}
	if got := table.Models(); !slices.Equal(got, []string{"empty", "qwen"}) {
		t.Fatalf("models = %v, want sorted [empty qwen]", got)
	}
}

// TestTableConcurrentReadWrite 在 -race 下才有意义：
// 写方高频 Replace，读方高频 Endpoints/Models。
// 原子快照保证读到的永远是某一完整历史快照，不会撕裂（更不会 panic）。
func TestTableConcurrentReadWrite(t *testing.T) {
	table := NewTable()
	snaps := []map[string][]string{
		{"qwen": {"a:8000"}},
		{"qwen": {"a:8000", "b:8000"}},
		{"qwen": {}},
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				table.Replace(snaps[i%len(snaps)])
			}
		}
	}()

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20000 {
				got := table.Endpoints("qwen")
				valid := len(got) == 0 ||
					slices.Equal(got, []string{"a:8000"}) ||
					slices.Equal(got, []string{"a:8000", "b:8000"})
				if !valid {
					t.Errorf("torn read: %v", got)
					return
				}
				_ = table.Models()
			}
		}()
	}

	close(stop)
	wg.Wait()
}
