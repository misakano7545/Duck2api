package initialize

import (
	"aurora/internal/duckgo"
	"aurora/internal/httpclient"
	"aurora/internal/httpclient/resty"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const duckDuckGoModelsURL = "https://duck.ai/duckchat/v1/models"

type duckDuckGoModel struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

type duckDuckGoModelsResponse struct {
	Models []duckDuckGoModel `json:"models"`
}

type openAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type openAIModelsResponse struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

func fetchDuckDuckGoModels(proxyURL string) (openAIModelsResponse, int, error) {
	client := resty.NewStdClient()
	if proxyURL != "" {
		if err := client.SetProxy(proxyURL); err != nil {
			return openAIModelsResponse{}, http.StatusBadGateway, fmt.Errorf("configure models proxy: %w", err)
		}
	}

	headers := make(httpclient.AuroraHeaders)
	headers.Set("accept", "application/json")
	headers.Set("origin", "https://duck.ai")
	headers.Set("referer", "https://duck.ai/")
	headers.Set("user-agent", duckgo.UA)

	response, err := client.Request(httpclient.GET, duckDuckGoModelsURL, headers, nil, nil)
	if err != nil {
		return openAIModelsResponse{}, http.StatusBadGateway, fmt.Errorf("request DuckDuckGo models: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return openAIModelsResponse{}, http.StatusBadGateway, fmt.Errorf("read DuckDuckGo models response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return openAIModelsResponse{}, response.StatusCode, fmt.Errorf("DuckDuckGo models returned %s: %s", response.Status, string(body))
	}

	models, err := parseDuckDuckGoModels(body, time.Now().Unix())
	if err != nil {
		return openAIModelsResponse{}, http.StatusBadGateway, err
	}
	return models, http.StatusOK, nil
}

func parseDuckDuckGoModels(body []byte, created int64) (openAIModelsResponse, error) {
	var upstream duckDuckGoModelsResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		return openAIModelsResponse{}, fmt.Errorf("decode DuckDuckGo models response: %w", err)
	}

	result := openAIModelsResponse{
		Object: "list",
		Data:   make([]openAIModel, 0, len(upstream.Models)+1),
	}
	for _, model := range upstream.Models {
		if model.ID == "" {
			continue
		}
		result.Data = append(result.Data, openAIModel{
			ID:      model.ID,
			Object:  "model",
			Created: created,
			OwnedBy: model.Provider,
		})
	}

	// 不在上游列表、但实测可请求的模型，补进去客户端才枚举得到：
	//   gpt-6-luna        可直接对话（上游列表里没有）
	//   image-generation  原生图片模型，/v1/images/generations 与改图的默认
	//   gpt-image-1.5/2   出图别名，见 duckgo.ResolveImageModel
	for _, hidden := range hiddenModels {
		if hasModelID(result.Data, hidden.id) {
			continue
		}
		result.Data = append(result.Data, openAIModel{
			ID:      hidden.id,
			Object:  "model",
			Created: created,
			OwnedBy: hidden.ownedBy,
		})
	}
	return result, nil
}

// hiddenModelID: DuckDuckGo 前端 bundle 里有、但 /duckchat/v1/models 不返回的可用模型。
const hiddenModelID = "gpt-6-luna"

// 上游列表里没有、但本网关实测可请求的模型。客户端靠 /v1/models 枚举，不在这里列出就点不到。
var hiddenModels = []struct{ id, ownedBy string }{
	{hiddenModelID, "openai"},
	{duckgo.NativeImageModel, "duck.ai"},
	{"gpt-image-1.5", "duck.ai"}, // 出图别名 → 原生图片模型
	{"gpt-image-2", "duck.ai"},   // 出图别名 → 聊天模型 + GenerateImage 工具
}

func hasModelID(models []openAIModel, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}
