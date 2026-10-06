package initialize

import (
	"aurora/httpclient"
	"aurora/httpclient/resty"
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
	headers.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")

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

	// gpt-6-luna 不在 DuckDuckGo 的 models 列表里, 但实测可直接对话, 补进去。
	if !hasModelID(result.Data, hiddenModelID) {
		result.Data = append(result.Data, openAIModel{
			ID:      hiddenModelID,
			Object:  "model",
			Created: created,
			OwnedBy: "openai",
		})
	}
	return result, nil
}

// hiddenModelID: DuckDuckGo 前端 bundle 里有、但 /duckchat/v1/models 不返回的可用模型。
const hiddenModelID = "gpt-6-luna"

func hasModelID(models []openAIModel, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}
