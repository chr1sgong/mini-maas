package openai

// Usage是OpenAI协议中用量相关的字段.
// - 非流式响应中Body里包含该结构
// - 流式响应(注入include_usage)末尾的usage chunk也是这个结构
// !!! 网关目前只需要这个结构,其余字段先不解析
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// /v1/models接口响应类型
type ModelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelList struct {
	Object string        `json:"object"`
	Data   []ModelObject `json:"data"`
}
