package metering

import "testing"

func TestParserNonStream(t *testing.T) {
	p := newParser(false)
	// 模拟非流式响应被切成多块到达
	p.feed([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":"hi"}}],`))
	p.feed([]byte(`"usage":{"prompt_tokens":10,"completion_tokens":50,"total_tokens":60}}`))
	u := p.finish()
	if u == nil || u.TotalTokens != 60 {
		t.Fatalf("usage = %+v, want total 60", u)
	}
}

func TestParserStreamUsageChunk(t *testing.T) {
	p := newParser(true)
	p.feed([]byte("data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n"))
	// usage chunk 被从中间切断，跨两块到达
	p.feed([]byte("data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\nda"))
	p.feed([]byte("ta: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\ndata: [DONE]\n\n"))
	u := p.finish()
	if u == nil || u.TotalTokens != 6 {
		t.Fatalf("usage = %+v, want total 6", u)
	}
}

func TestParserStreamNoUsage(t *testing.T) {
	p := newParser(true)
	p.feed([]byte("data: {\"id\":\"x\",\"choices\":[{\"delta\":{}}]}\n\ndata: [DONE]\n\n"))
	if u := p.finish(); u != nil {
		t.Fatalf("usage = %+v, want nil", u)
	}
}
