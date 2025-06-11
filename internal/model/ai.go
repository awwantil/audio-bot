package model

// Структуры для API Chat Completions
type ChatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		// Могут быть и другие поля, например, finish_reason, но для задачи нужен только content
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
	// Можно добавить другие поля если нужно, например, Usage
}

// Структура для разбора JSON-ответа от API распознавания речи
type TranscriptionResponse struct {
	Text  string `json:"text"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}
