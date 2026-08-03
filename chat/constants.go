package chat

// ==================== Delta 类型 ====================

const (
	DeltaTypeText      = "text_delta"       // 文本增量
	DeltaTypeThinking  = "thinking_delta"   // 思考链增量
	DeltaTypeInputJSON = "input_json_delta" // 工具入参 JSON 增量
)
