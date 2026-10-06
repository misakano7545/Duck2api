package initialize

import (
	"testing"
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
	if len(result.Data) != 3 { // 2 个上游模型 + 补进来的 gpt-6-luna
		t.Fatalf("expected 3 models, got %d", len(result.Data))
	}
	if result.Data[0].ID != "gpt-5.4" || result.Data[0].OwnedBy != "openai" || result.Data[0].Created != 123 {
		t.Fatalf("unexpected first model: %+v", result.Data[0])
	}
	if result.Data[1].ID != "mistral-small-2603" || result.Data[1].OwnedBy != "mistral" {
		t.Fatalf("unexpected second model: %+v", result.Data[1])
	}
	if result.Data[2].ID != hiddenModelID {
		t.Fatalf("expected hidden model %q appended, got %+v", hiddenModelID, result.Data[2])
	}
	// 上游若哪天真的返回了它, 不能重复
	dup, err := parseDuckDuckGoModels([]byte(`{"models":[{"id":"gpt-6-luna","provider":"openai"}]}`), 123)
	if err != nil || len(dup.Data) != 1 {
		t.Fatalf("hidden model must not be duplicated: %+v (err=%v)", dup.Data, err)
	}
}

func TestParseDuckDuckGoModelsRejectsInvalidJSON(t *testing.T) {
	if _, err := parseDuckDuckGoModels([]byte(`{"models":`), 123); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}
