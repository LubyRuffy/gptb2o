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
