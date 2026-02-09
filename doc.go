// Package gptb2o 提供将 ChatGPT Backend API（基于 OAuth token 的 responses SSE 接口）
// 转换为 OpenAI 兼容 API 的能力，方便第三方程序以 OpenAI SDK 的方式调用，
// 从而在订阅模式下节省 APIKey 成本。
//
// 该仓库主要包含两类能力：
//  1. HTTP 兼容层：openaihttp 包导出 /v1/models、/v1/chat/completions、/v1/responses handlers
//  2. SDK：backend 包提供可供 Eino/ADK 使用的 ToolCallingChatModel 实现
package gptb2o
