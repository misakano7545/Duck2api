package duckgo

import "encoding/json"

type ImagePartData struct {
	B64Image string `json:"b64Image"`
	Format   string `json:"format"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Status   string `json:"status,omitempty"`
	Title    string `json:"title,omitempty"`
	Type     string `json:"type,omitempty"`
}

type ImagePart struct {
	Type   string         `json:"type"`
	Result string         `json:"result,omitempty"`
	Format string         `json:"format,omitempty"`
	Width  int            `json:"width,omitempty"`
	Height int            `json:"height,omitempty"`
	Data   *ImagePartData `json:"data,omitempty"`
}

type ApiResponse struct {
	Message    string `json:"message"`
	Created    int    `json:"created"`
	Id         string `json:"id"`
	Action     string `json:"action"`
	Model      string `json:"model"`
	Role       string `json:"role,omitempty"`
	State      string `json:"state,omitempty"`
	Name       string `json:"name,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	ToolCallId string `json:"toolCallId,omitempty"`
	// ToolArguments 是上游工具调用的入参原文（JSON 字符串）。出图时里面是
	// {"imageGenPrompt":"..."}——即 duck.ai 转手喂给图像服务（gpt-image-2）的真实提示词，
	// 用户给的原始 prompt 会被上游改写后再送进去。抓它才拿得到"实际画的那句话"。
	ToolArguments string          `json:"toolArguments,omitempty"`
	Result        string          `json:"result,omitempty"`
	Parts         []ImagePart     `json:"parts,omitempty"`
	Data          json.RawMessage `json:"data,omitempty"`
}

// GetImageGenPrompt 取出出图工具入参里的真实提示词（上游改写后的），无则空串。
func (r *ApiResponse) GetImageGenPrompt() string {
	if r.ToolArguments == "" {
		return ""
	}
	var a struct {
		ImageGenPrompt string `json:"imageGenPrompt"`
	}
	if err := json.Unmarshal([]byte(r.ToolArguments), &a); err != nil {
		return ""
	}
	return a.ImageGenPrompt
}

// GetImageData extracts b64Image from the data field
func (r *ApiResponse) GetImageData() *ImagePartData {
	if r.Data == nil {
		return nil
	}
	var d ImagePartData
	if err := json.Unmarshal(r.Data, &d); err != nil {
		return nil
	}
	if d.B64Image == "" {
		return nil
	}
	return &d
}
