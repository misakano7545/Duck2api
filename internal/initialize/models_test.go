package initialize

import (
	"strings"
	"testing"

	"aurora/internal/duckgo"
)

func TestParseDuckDuckGoModels(t *testing.T) {
	body := []byte(`{"models":[{"id":"gpt-5.4","provider":"openai"},{"id":"mistral-small-2603","provider":"mistral"},{"id":"","provider":"ignored"}]}`)

	result, err := parseDuckDuckGoModels(body, 123)
	if err != nil {
		t.Fatalf("parseDuckDuckGoModels returned error: %v", err)
	}
	if result.Object != "list" {
		t.Fatalf("unexpected object: %q", result.Object)
	}
	if len(result.Data) != 2+len(hiddenModels) { // 2 个上游模型 + 补进来的隐藏模型
		t.Fatalf("expected %d models, got %d", 2+len(hiddenModels), len(result.Data))
	}
	if result.Data[0].ID != "gpt-5.4" || result.Data[0].OwnedBy != "openai" || result.Data[0].Created != 123 {
		t.Fatalf("unexpected first model: %+v", result.Data[0])
	}
	if result.Data[1].ID != "mistral-small-2603" || result.Data[1].OwnedBy != "mistral" {
		t.Fatalf("unexpected second model: %+v", result.Data[1])
	}
	// 隐藏模型必须都在枚举里，且出图别名真的被 ResolveImageModel 接住
	// （没接住的话客户端照列表点过去，上游只会 404 ERR_MODEL_UNAVAILABLE）
	for _, h := range hiddenModels {
		if !hasModelID(result.Data, h.id) {
			t.Fatalf("hidden model %q 没被补进列表: %+v", h.id, result.Data)
		}
		if strings.HasPrefix(h.id, "gpt-image-") {
			_, real := duckgo.ResolveImageModel(h.id)
			if real != duckgo.NativeImageModel && real != duckgo.ToolImageChatModel {
				t.Errorf("别名 %q 没被 ResolveImageModel 接住，会原样透传上游", h.id)
			}
		}
	}
	if !hasModelID(result.Data, duckgo.NativeImageModel) {
		t.Fatalf("原生图片模型没被补进列表: %+v", result.Data)
	}
	// 上游若哪天真的返回了它们, 不能重复（只有列表里没有的才补，总数仍是 len(hiddenModels)）
	dup, err := parseDuckDuckGoModels([]byte(`{"models":[{"id":"gpt-6-luna","provider":"openai"},{"id":"image-generation","provider":"duck.ai"}]}`), 123)
	if err != nil || len(dup.Data) != len(hiddenModels) {
		t.Fatalf("hidden models must not be duplicated: %+v (err=%v)", dup.Data, err)
	}
	ids := make([]string, 0, len(dup.Data))
	for _, m := range dup.Data {
		ids = append(ids, m.ID)
	}
	if n := strings.Count(strings.Join(ids, ","), "gpt-6-luna"); n != 1 {
		t.Fatalf("gpt-6-luna 出现 %d 次: %v", n, ids)
	}
}

func TestParseDuckDuckGoModelsRejectsInvalidJSON(t *testing.T) {
	if _, err := parseDuckDuckGoModels([]byte(`{"models":`), 123); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}
