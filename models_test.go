package gptb2o_test

import (
	"testing"

	"github.com/LubyRuffy/gptb2o"
)

func TestPresetModels_DefaultFirst(t *testing.T) {
	models := gptb2o.PresetModels()
	if len(models) == 0 {
		t.Fatalf("PresetModels() should not be empty")
	}

	if models[0].ID != gptb2o.DefaultModelFullID {
		t.Fatalf("first model = %q, want %q", models[0].ID, gptb2o.DefaultModelFullID)
	}
}

func TestDefaultModel_IsSupported(t *testing.T) {
	if !gptb2o.IsSupportedModelID(gptb2o.DefaultModelID) {
		t.Fatalf("default model id %q should be supported", gptb2o.DefaultModelID)
	}

	if !gptb2o.IsSupportedModelID(gptb2o.DefaultModelFullID) {
		t.Fatalf("default model full id %q should be supported", gptb2o.DefaultModelFullID)
	}
}

func TestPresetModels_ContainsLatestModel(t *testing.T) {
	target := gptb2o.ModelNamespace + "gpt-5.5"
	if !gptb2o.IsSupportedModelID(target) {
		t.Fatalf("latest model id %q should be supported", target)
	}

	found := false

	for _, m := range gptb2o.PresetModels() {
		if m.ID == target {
			found = true

			break
		}
	}

	if !found {
		t.Fatalf("latest model %q should be in preset models list", target)
	}
}

func TestPresetModels_ContainsGPT54Mini(t *testing.T) {
	target := gptb2o.ModelNamespace + "gpt-5.4-mini"
	if !gptb2o.IsSupportedModelID(target) {
		t.Fatalf("model id %q should be supported", target)
	}

	found := false

	for _, m := range gptb2o.PresetModels() {
		if m.ID == target {
			found = true

			break
		}
	}

	if !found {
		t.Fatalf("model %q should be in preset models list", target)
	}
}

func TestPresetModels_DoesNotContainGPT54Nano(t *testing.T) {
	target := gptb2o.ModelNamespace + "gpt-5.4-nano"
	if gptb2o.IsSupportedModelID(target) {
		t.Fatalf("model id %q should not be supported", target)
	}

	for _, m := range gptb2o.PresetModels() {
		if m.ID == target {
			t.Fatalf("model %q should not be in preset models list", target)
		}
	}
}

func TestPresetModels_DoesNotContainGPT51Family(t *testing.T) {
	unsupportedModels := []string{
		"gpt-5.1",
		"gpt-5.1-codex",
		"gpt-5.1-codex-mini",
		"gpt-5.1-codex-max",
	}

	for _, modelID := range unsupportedModels {
		fullID := gptb2o.ModelNamespace + modelID
		if gptb2o.IsSupportedModelID(fullID) {
			t.Fatalf("model id %q should not be supported", fullID)
		}

		for _, presetModel := range gptb2o.PresetModels() {
			if presetModel.ID == fullID {
				t.Fatalf("model %q should not be in preset models list", fullID)
			}
		}
	}
}
