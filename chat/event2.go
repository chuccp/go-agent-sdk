package chat

type Event2 struct {
	SourceType SourceType
	Seq        uint   `json:"seq"`
	StartType  string `json:"type"`
	Block      Block  `json:"block"`
}
