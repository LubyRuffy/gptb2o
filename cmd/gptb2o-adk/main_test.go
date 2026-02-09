package main

import "testing"

func TestDefaultAgentNameNotEmpty(t *testing.T) {
	// 防止回归：eino/adk 的 ChatModelAgentConfig 要求 Name 必填。
	if defaultAgentName == "" {
		t.Fatalf("defaultAgentName should not be empty")
	}
}
