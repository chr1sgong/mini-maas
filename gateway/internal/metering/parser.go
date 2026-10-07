package metering

import (
	"bytes"
	"encoding/json"

	"github.com/mini-maas/gateway/internal/openai"
)

type usageEvenlope struct {
	Usage *openai.Usage `json:"usage"`
}

type parser struct {
	sse  bool
	buf  bytes.Buffer
	used *openai.Usage
}

func newParser(sse bool) *parser {
	return &parser{
		sse: sse,
	}
}

func (p *parser) feed(chunk []byte) {
	if p.used != nil {
		// 已经拿到usage了,后续数据不需要了
		return
	}

	p.buf.Write(chunk)
	if !p.sse {
		return
	}
	for {
		line, err := p.buf.ReadBytes('\n')
		if err != nil {
			p.buf.Write(line)
			return
		}
		p.parseLine(bytes.TrimSpace(line))
		if p.used != nil {
			return
		}
	}
}

func (p *parser) parseLine(line []byte) {
	data, ok := bytes.CutPrefix(line, []byte("data: "))
	if !ok || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	if !bytes.Contains(data, []byte("usage")) {
		return
	}
	var env usageEvenlope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	p.used = env.Usage
}

// 拿不到usage直接返回nil, 由调用方决定是否降级
func (p *parser) finish() *openai.Usage {
	if p.used == nil && !p.sse && p.buf.Len() > 0 {
		var env usageEvenlope
		if err := json.Unmarshal(p.buf.Bytes(), &env); err == nil {
			p.used = env.Usage
		}
	}
	return p.used
}
