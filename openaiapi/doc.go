// Package openaiapi 提供 OpenAI v1 兼容接口的通用数据结构与辅助函数。
//
// 该包只关注协议层：请求/响应 JSON 结构、SSE chunk 结构、错误结构以及少量构建函数。
// 业务侧（例如 ChatGPT backend / LlamaCpp 等适配）应在其他包中实现。
//
// 示例：创建一个 SSE chunk 并序列化输出
//
//	chunk := openaiapi.ToChatChunk("chatcmpl-xxx", "gpt-4.1", "hello", nil, "fp_example")
//	_ = json.NewEncoder(w).Encode(chunk)
package openaiapi
