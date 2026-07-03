package usage

type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

func (u Usage) Total() int64 {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.PromptTokens + u.CompletionTokens
}
