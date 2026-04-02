package gptb2o

import "testing"

func TestPresetModels_DefaultFirst(t *testing.T) {
	models := PresetModels()
	if len(models) == 0 {
		t.Fatalf("PresetModels() should not be empty")
	}

	if models[0].ID != DefaultModelFullID {
		t.Fatalf("first model = %q, want %q", models[0].ID, DefaultModelFullID)
	}
}

func TestDefaultModel_IsSupported(t *testing.T) {
	if !IsSupportedModelID(DefaultModelID) {
		t.Fatalf("default model id %q should be supported", DefaultModelID)
	}
	if !IsSupportedModelID(DefaultModelFullID) {
		t.Fatalf("default model full id %q should be supported", DefaultModelFullID)
	}
}

func TestPresetModels_ContainsLatestModel(t *testing.T) {
	target := ModelNamespace + "gpt-5.4"
	if !IsSupportedModelID(target) {
		t.Fatalf("latest model id %q should be supported", target)
	}

	found := false
	for _, m := range PresetModels() {
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
	target := ModelNamespace + "gpt-5.4-mini"
	if !IsSupportedModelID(target) {
		t.Fatalf("model id %q should be supported", target)
	}

	found := false
	for _, m := range PresetModels() {
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
	target := ModelNamespace + "gpt-5.4-nano"
	if IsSupportedModelID(target) {
		t.Fatalf("model id %q should not be supported", target)
	}

	for _, m := range PresetModels() {
		if m.ID == target {
			t.Fatalf("model %q should not be in preset models list", target)
		}
	}
}
