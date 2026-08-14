package chat

import (
	"encoding/json"
	"testing"
)

// 注：UnmarshalBlock / Blocks.UnmarshalJSON 目前被注释掉（与 *value.Object 入参不兼容），
// 对应的往返反序列化测试暂移除；仅保留 Marshal 方向的测试。

func TestBlocks_NilMarshalAsNull(t *testing.T) {
	var bs Blocks
	data, err := json.Marshal(bs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "null" {
		t.Errorf("expected null for nil Blocks, got %s", string(data))
	}
}

func TestBlocks_EmptyMarshalAsArray(t *testing.T) {
	bs := make(Blocks, 0)
	data, err := json.Marshal(bs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[]" {
		t.Errorf("expected [] for empty Blocks, got %s", string(data))
	}
}
